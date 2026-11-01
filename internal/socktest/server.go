// Package socktest is an in-process fake sock server for tests. It implements
// the generated Connect handlers so clients are exercised over real HTTP.
package socktest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"
	"github.com/aeryx-ai/sock-contract/gen/go/sock/v1/sockv1connect"
)

// Token is the puppet bearer token the fake accepts.
const Token = "sock_pt_testdevice_testsecret"

// MachineKeyToken is a machine key the fake accepts.
const MachineKeyToken = "sock_mk_mkdevice_mksecret"

// HumanDeviceID is the device inside HumanToken.
const HumanDeviceID = "0a0a0a0a-0000-4000-8000-000000000001"

// HumanToken is the human session token the fake accepts and issues.
const HumanToken = "sock_st_" + HumanDeviceID + "_humansecret"

// anonymous are the methods the real server admits without a token.
var anonymous = map[string]bool{"SignInWithApple": true, "RegisterWithApple": true}

// Call is one unary request the fake received.
type Call struct {
	Method string // e.g. "SignInWithApple"
	Msg    proto.Message
	Header http.Header
}

// Sent records one NotifyOwner or Send call.
type Sent struct {
	ConversationID string // empty for NotifyOwner
	Text           string
	IdempotencyKey string
}

// StreamScript is what one Subscribe connection does: send Events in order,
// then end with End (nil means a clean close). If Hold is true the stream
// stays open after Events until the client goes away, sending a heartbeat
// every HeartbeatEvery when that is set.
type StreamScript struct {
	Events         []*sockv1.SubscribeResponse
	End            error
	Hold           bool
	HeartbeatEvery time.Duration
}

// Server is the fake. Set fields before the client calls; read them after.
type Server struct {
	URL string

	mu            sync.Mutex
	Me            *sockv1.Account
	Conversations []*sockv1.Conversation
	Pending       []*sockv1.Message
	Acked         []string
	Sent          []Sent
	UserAgents    []string
	// SendErrs are returned, front first, by NotifyOwner and Send before
	// they start succeeding.
	SendErrs []error
	// AckErrs are returned, front first, by Ack before it starts
	// succeeding. A failed Ack acknowledges nothing.
	AckErrs []error
	// Streams are consumed, front first, one per Subscribe call. With none
	// left, Subscribe holds the stream open until the client cancels.
	Streams []StreamScript
	// KeepAcked leaves acked messages pending, like a server whose acks
	// silently do nothing.
	KeepAcked bool
	// Subscribes counts Subscribe calls.
	Subscribes int

	// Calls records every unary request, in order.
	Calls []Call
	// Fail makes a method (e.g. "SignInWithApple") return its error every
	// time, after the call is recorded.
	Fail map[string]error
	// Session is what SignInWithApple and RegisterWithApple return.
	Session *sockv1.Session
	// AcceptedTerms and CurrentTerms are GetMe's terms versions.
	AcceptedTerms, CurrentTerms string
	// Accounts are what GetAccounts and LookupHandle search.
	Accounts []*sockv1.Account
	// Devices are ListDevices' answer; Puppets ListPuppets'; Blocked
	// ListBlockedAccounts'.
	Devices []*sockv1.Device
	Puppets []*sockv1.PuppetWithDevices
	// MachineKeyPuppets, when set, is ListPuppets' answer to MachineKeyToken:
	// the puppets that key created.
	MachineKeyPuppets []*sockv1.PuppetWithDevices
	// MintMachineKeyErr, when set, is MintMachineKey's answer.
	MintMachineKeyErr error
	Blocked           []*sockv1.BlockedAccount

	sockv1connect.UnimplementedIdentityServiceHandler
	sockv1connect.UnimplementedMessagingServiceHandler
	sockv1connect.UnimplementedModerationServiceHandler
}

// Human is the account HumanToken belongs to.
func Human() *sockv1.Account {
	return &sockv1.Account{Id: "human-1", DisplayName: "Guy", Kind: &sockv1.Account_Human{Human: &sockv1.Human{Handle: "guy"}}}
}

// New starts the fake and stops it when the test ends.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		Me: &sockv1.Account{
			Id:          "puppet-1",
			DisplayName: "Build Bot",
			Kind:        &sockv1.Account_Puppet{Puppet: &sockv1.Puppet{OwnerId: "human-1", Name: "build-bot"}},
		},
	}
	s.Session = &sockv1.Session{
		Account: Human(),
		Device:  &sockv1.Device{Id: HumanDeviceID, AccountId: "human-1", Label: "sock CLI on test"},
		Token:   HumanToken,
	}
	s.CurrentTerms, s.AcceptedTerms = "2026-10-11", "2026-10-11"
	mux := http.NewServeMux()
	opts := connect.WithInterceptors(connect.UnaryInterceptorFunc(s.intercept))
	idPath, idHandler := sockv1connect.NewIdentityServiceHandler(s, opts)
	msgPath, msgHandler := sockv1connect.NewMessagingServiceHandler(s, opts)
	modPath, modHandler := sockv1connect.NewModerationServiceHandler(s, opts)
	mux.Handle(idPath, s.auth(idHandler))
	mux.Handle(msgPath, s.auth(msgHandler))
	mux.Handle(modPath, s.auth(modHandler))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.UserAgents = append(s.UserAgents, r.Header.Get("User-Agent"))
		s.mu.Unlock()
		if anonymous[path.Base(r.URL.Path)] {
			next.ServeHTTP(w, r)
			return
		}
		if a := r.Header.Get("Authorization"); a != "Bearer "+Token && a != "Bearer "+HumanToken && a != "Bearer "+MachineKeyToken && a != "Bearer sock_pt_device-new_secret" {
			err := withReason(connect.NewError(connect.CodeUnauthenticated, errors.New("token rejected")), sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID)
			_ = connect.NewErrorWriter().Write(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// intercept records each unary call and applies Fail.
func (s *Server) intercept(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		method := path.Base(req.Spec().Procedure)
		s.mu.Lock()
		msg, _ := req.Any().(proto.Message)
		s.Calls = append(s.Calls, Call{Method: method, Msg: proto.Clone(msg), Header: req.Header().Clone()})
		err := s.Fail[method]
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// CallsTo returns the recorded requests to method.
func (s *Server) CallsTo(method string) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, c := range s.Calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Error builds a refusal carrying reason, as the real server sends it.
func Error(code connect.Code, reason sockv1.ErrorReason) error {
	return withReason(connect.NewError(code, errors.New(strings.ToLower(reason.String()))), reason)
}

func withReason(ce *connect.Error, reason sockv1.ErrorReason) *connect.Error {
	d, err := connect.NewErrorDetail(&sockv1.ErrorInfo{Reason: reason})
	if err != nil {
		panic(err)
	}
	ce.AddDetail(d)
	return ce
}

// RateLimited is the error the real server returns when a puppet sends too fast.
func RateLimited(retryAfter string) error {
	ce := withReason(connect.NewError(connect.CodeResourceExhausted, errors.New("rate limited")), sockv1.ErrorReason_ERROR_REASON_RATE_LIMITED)
	ce.Meta().Set("Retry-After", retryAfter)
	return ce
}

// Unavailable is a transient server failure.
func Unavailable() error {
	return connect.NewError(connect.CodeUnavailable, errors.New("database unreachable"))
}

// NotMember is the permission error for a conversation the caller is not in.
func NotMember() error {
	return withReason(connect.NewError(connect.CodePermissionDenied, errors.New("not a member")), sockv1.ErrorReason_ERROR_REASON_NOT_A_MEMBER)
}

// Text builds a text message.
func Text(id, conversationID string, seq int64, text string) *sockv1.Message {
	return &sockv1.Message{
		Id:             id,
		ConversationId: conversationID,
		Seq:            seq,
		SenderId:       "human-1",
		SentAt:         timestamppb.New(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)),
		Body:           &sockv1.Message_Text{Text: text},
	}
}

// Event wraps a message as a stream event.
func Event(m *sockv1.Message) *sockv1.SubscribeResponse {
	return &sockv1.SubscribeResponse{Event: &sockv1.SubscribeResponse_Message{Message: m}}
}

// Heartbeat is a keepalive stream event.
func Heartbeat() *sockv1.SubscribeResponse {
	return &sockv1.SubscribeResponse{Event: &sockv1.SubscribeResponse_Heartbeat{Heartbeat: &sockv1.Heartbeat{}}}
}

// GetMe implements the generated handler.
func (s *Server) GetMe(_ context.Context, _ *connect.Request[sockv1.GetMeRequest]) (*connect.Response[sockv1.GetMeResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return connect.NewResponse(&sockv1.GetMeResponse{Account: s.Me, AcceptedTermsVersion: s.AcceptedTerms, CurrentTermsVersion: s.CurrentTerms}), nil
}

// ListConversations implements the generated handler.
func (s *Server) ListConversations(_ context.Context, _ *connect.Request[sockv1.ListConversationsRequest]) (*connect.Response[sockv1.ListConversationsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return connect.NewResponse(&sockv1.ListConversationsResponse{Conversations: s.Conversations}), nil
}

func (s *Server) record(conv, text, key string) (*sockv1.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Sent = append(s.Sent, Sent{ConversationID: conv, Text: text, IdempotencyKey: key})
	if len(s.SendErrs) > 0 {
		err := s.SendErrs[0]
		s.SendErrs = s.SendErrs[1:]
		return nil, err
	}
	if conv == "" {
		conv = "owner-dm"
	}
	n := int64(len(s.Sent))
	return Text(fmt.Sprintf("m-%d", n), conv, n, text), nil
}

// NotifyOwner implements the generated handler.
func (s *Server) NotifyOwner(_ context.Context, req *connect.Request[sockv1.NotifyOwnerRequest]) (*connect.Response[sockv1.NotifyOwnerResponse], error) {
	m, err := s.record("", req.Msg.GetText(), req.Msg.GetIdempotencyKey())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&sockv1.NotifyOwnerResponse{Message: m}), nil
}

// Send implements the generated handler.
func (s *Server) Send(_ context.Context, req *connect.Request[sockv1.SendRequest]) (*connect.Response[sockv1.SendResponse], error) {
	m, err := s.record(req.Msg.GetConversationId(), req.Msg.GetText(), req.Msg.GetIdempotencyKey())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&sockv1.SendResponse{Message: m}), nil
}

// FetchPending implements the generated handler.
func (s *Server) FetchPending(_ context.Context, req *connect.Request[sockv1.FetchPendingRequest]) (*connect.Response[sockv1.FetchPendingResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := min(int(req.Msg.GetLimit()), len(s.Pending))
	return connect.NewResponse(&sockv1.FetchPendingResponse{Messages: slices.Clone(s.Pending[:n])}), nil
}

// Ack implements the generated handler.
func (s *Server) Ack(_ context.Context, req *connect.Request[sockv1.AckRequest]) (*connect.Response[sockv1.AckResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.AckErrs) > 0 {
		err := s.AckErrs[0]
		s.AckErrs = s.AckErrs[1:]
		return nil, err
	}
	for _, id := range req.Msg.GetMessageIds() {
		s.Acked = append(s.Acked, id)
		if s.KeepAcked {
			continue
		}
		s.Pending = slices.DeleteFunc(s.Pending, func(m *sockv1.Message) bool { return m.GetId() == id })
	}
	return connect.NewResponse(&sockv1.AckResponse{}), nil
}

// Subscribe implements the generated handler.
func (s *Server) Subscribe(ctx context.Context, _ *connect.Request[sockv1.SubscribeRequest], stream *connect.ServerStream[sockv1.SubscribeResponse]) error {
	s.mu.Lock()
	s.Subscribes++
	var script StreamScript
	if len(s.Streams) > 0 {
		script = s.Streams[0]
		s.Streams = s.Streams[1:]
	} else {
		script = StreamScript{Hold: true}
	}
	s.mu.Unlock()
	for _, ev := range script.Events {
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	if script.Hold {
		var tick <-chan time.Time
		if script.HeartbeatEvery > 0 {
			t := time.NewTicker(script.HeartbeatEvery)
			defer t.Stop()
			tick = t.C
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tick:
				if err := stream.Send(Heartbeat()); err != nil {
					return err
				}
			}
		}
	}
	return script.End
}

// Snapshot returns copies of the recorded state, safe to read while the
// server is running.
func (s *Server) Snapshot() (sent []Sent, acked []string, userAgents []string, subscribes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.Sent), slices.Clone(s.Acked), slices.Clone(s.UserAgents), s.Subscribes
}
