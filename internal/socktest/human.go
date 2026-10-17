package socktest

import (
	"context"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"
)

// The human RPCs. Each answers from the Server's fields; tests read what
// was sent from Calls.

//nolint:unparam // the nil error lets handlers return ok(...) directly
func ok[T any](m *T) (*connect.Response[T], error) { return connect.NewResponse(m), nil }

func (s *Server) session() *sockv1.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.Clone(s.Session).(*sockv1.Session)
}

// SignInWithApple implements the generated handler.
func (s *Server) SignInWithApple(context.Context, *connect.Request[sockv1.SignInWithAppleRequest]) (*connect.Response[sockv1.SignInWithAppleResponse], error) {
	return ok(&sockv1.SignInWithAppleResponse{Session: s.session()})
}

// RegisterWithApple implements the generated handler.
func (s *Server) RegisterWithApple(context.Context, *connect.Request[sockv1.RegisterWithAppleRequest]) (*connect.Response[sockv1.RegisterWithAppleResponse], error) {
	return ok(&sockv1.RegisterWithAppleResponse{Session: s.session()})
}

// SignOut implements the generated handler.
func (s *Server) SignOut(context.Context, *connect.Request[sockv1.SignOutRequest]) (*connect.Response[sockv1.SignOutResponse], error) {
	return ok(&sockv1.SignOutResponse{})
}

// AcceptTerms implements the generated handler.
func (s *Server) AcceptTerms(_ context.Context, req *connect.Request[sockv1.AcceptTermsRequest]) (*connect.Response[sockv1.AcceptTermsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Msg.GetTermsVersion() != s.CurrentTerms {
		return nil, Error(connect.CodeInvalidArgument, sockv1.ErrorReason_ERROR_REASON_FIELD_INVALID)
	}
	s.AcceptedTerms = s.CurrentTerms
	return ok(&sockv1.AcceptTermsResponse{})
}

// GetAccounts implements the generated handler.
func (s *Server) GetAccounts(_ context.Context, req *connect.Request[sockv1.GetAccountsRequest]) (*connect.Response[sockv1.GetAccountsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*sockv1.Account
	for _, a := range s.Accounts {
		if slices.Contains(req.Msg.GetIds(), a.GetId()) {
			out = append(out, a)
		}
	}
	return ok(&sockv1.GetAccountsResponse{Accounts: out})
}

// LookupHandle implements the generated handler.
func (s *Server) LookupHandle(_ context.Context, req *connect.Request[sockv1.LookupHandleRequest]) (*connect.Response[sockv1.LookupHandleResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.Accounts {
		if a.GetHuman() != nil && a.GetHuman().GetHandle() == strings.ToLower(req.Msg.GetHandle()) {
			return ok(&sockv1.LookupHandleResponse{Account: a})
		}
	}
	return nil, Error(connect.CodeNotFound, sockv1.ErrorReason_ERROR_REASON_NOT_FOUND)
}

// UpdateDisplayName implements the generated handler.
func (s *Server) UpdateDisplayName(_ context.Context, req *connect.Request[sockv1.UpdateDisplayNameRequest]) (*connect.Response[sockv1.UpdateDisplayNameResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Me.DisplayName = req.Msg.GetDisplayName()
	return ok(&sockv1.UpdateDisplayNameResponse{Account: s.Me})
}

// ChangeHandle implements the generated handler.
func (s *Server) ChangeHandle(_ context.Context, req *connect.Request[sockv1.ChangeHandleRequest]) (*connect.Response[sockv1.ChangeHandleResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Me.Kind = &sockv1.Account_Human{Human: &sockv1.Human{Handle: req.Msg.GetHandle()}}
	return ok(&sockv1.ChangeHandleResponse{Account: s.Me})
}

// ListDevices implements the generated handler.
func (s *Server) ListDevices(context.Context, *connect.Request[sockv1.ListDevicesRequest]) (*connect.Response[sockv1.ListDevicesResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ok(&sockv1.ListDevicesResponse{Devices: s.Devices})
}

// RevokeDevice implements the generated handler.
func (s *Server) RevokeDevice(context.Context, *connect.Request[sockv1.RevokeDeviceRequest]) (*connect.Response[sockv1.RevokeDeviceResponse], error) {
	return ok(&sockv1.RevokeDeviceResponse{})
}

// Device builds a device created at a fixed time.
func Device(id, accountID, label string) *sockv1.Device {
	return &sockv1.Device{Id: id, AccountId: accountID, Label: label, CreatedAt: timestamppb.New(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))}
}

// Puppet builds a puppet account owned by human-1.
func Puppet(id, name string) *sockv1.Account {
	return &sockv1.Account{Id: id, DisplayName: name, Kind: &sockv1.Account_Puppet{Puppet: &sockv1.Puppet{OwnerId: "human-1", Name: name}}}
}

// CreatePuppet implements the generated handler.
func (s *Server) CreatePuppet(_ context.Context, req *connect.Request[sockv1.CreatePuppetRequest]) (*connect.Response[sockv1.CreatePuppetResponse], error) {
	return ok(&sockv1.CreatePuppetResponse{
		Puppet: Puppet("puppet-new", req.Msg.GetName()),
		Device: Device("device-new", "puppet-new", req.Msg.GetTokenLabel()),
		Token:  "sock_pt_device-new_secret",
	})
}

// MintMachineKey implements the generated handler.
func (s *Server) MintMachineKey(_ context.Context, req *connect.Request[sockv1.MintMachineKeyRequest]) (*connect.Response[sockv1.MintMachineKeyResponse], error) {
	if s.MintMachineKeyErr != nil {
		return nil, s.MintMachineKeyErr
	}
	return ok(&sockv1.MintMachineKeyResponse{
		Device: Device("device-mk", "human-1", req.Msg.GetLabel()),
		Token:  "sock_mk_device-mk_secret",
	})
}

// ListPuppets implements the generated handler.
func (s *Server) ListPuppets(_ context.Context, req *connect.Request[sockv1.ListPuppetsRequest]) (*connect.Response[sockv1.ListPuppetsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.MachineKeyPuppets != nil && req.Header().Get("Authorization") == "Bearer "+MachineKeyToken {
		return ok(&sockv1.ListPuppetsResponse{Puppets: s.MachineKeyPuppets})
	}
	return ok(&sockv1.ListPuppetsResponse{Puppets: s.Puppets})
}

// MintPuppetToken implements the generated handler.
func (s *Server) MintPuppetToken(_ context.Context, req *connect.Request[sockv1.MintPuppetTokenRequest]) (*connect.Response[sockv1.MintPuppetTokenResponse], error) {
	return ok(&sockv1.MintPuppetTokenResponse{
		Device: Device("device-minted", req.Msg.GetPuppetId(), req.Msg.GetLabel()),
		Token:  "sock_pt_device-minted_secret",
	})
}

// ErasePuppet implements the generated handler.
func (s *Server) ErasePuppet(context.Context, *connect.Request[sockv1.ErasePuppetRequest]) (*connect.Response[sockv1.ErasePuppetResponse], error) {
	return ok(&sockv1.ErasePuppetResponse{})
}

// ExportIdentityData implements the generated handler.
func (s *Server) ExportIdentityData(context.Context, *connect.Request[sockv1.ExportIdentityDataRequest]) (*connect.Response[sockv1.ExportIdentityDataResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ok(&sockv1.ExportIdentityDataResponse{Account: s.Me, Devices: s.Devices, Puppets: s.Puppets})
}

// EraseAccount implements the generated handler.
func (s *Server) EraseAccount(context.Context, *connect.Request[sockv1.EraseAccountRequest]) (*connect.Response[sockv1.EraseAccountResponse], error) {
	return ok(&sockv1.EraseAccountResponse{})
}

func conversation(id string, kind sockv1.ConversationKind, members ...string) *sockv1.Conversation {
	return &sockv1.Conversation{Id: id, Kind: kind, MemberIds: members}
}

// CreateDirect implements the generated handler.
func (s *Server) CreateDirect(_ context.Context, req *connect.Request[sockv1.CreateDirectRequest]) (*connect.Response[sockv1.CreateDirectResponse], error) {
	return ok(&sockv1.CreateDirectResponse{Conversation: conversation("conv-direct", sockv1.ConversationKind_CONVERSATION_KIND_DIRECT, "human-1", req.Msg.GetPeerId())})
}

// CreateGroup implements the generated handler.
func (s *Server) CreateGroup(_ context.Context, req *connect.Request[sockv1.CreateGroupRequest]) (*connect.Response[sockv1.CreateGroupResponse], error) {
	return ok(&sockv1.CreateGroupResponse{Conversation: conversation("conv-group", sockv1.ConversationKind_CONVERSATION_KIND_GROUP, append([]string{"human-1"}, req.Msg.GetMemberIds()...)...)})
}

// AddMember implements the generated handler.
func (s *Server) AddMember(_ context.Context, req *connect.Request[sockv1.AddMemberRequest]) (*connect.Response[sockv1.AddMemberResponse], error) {
	return ok(&sockv1.AddMemberResponse{Conversation: conversation(req.Msg.GetConversationId(), sockv1.ConversationKind_CONVERSATION_KIND_GROUP, "human-1", req.Msg.GetAccountId())})
}

// RemoveMember implements the generated handler.
func (s *Server) RemoveMember(_ context.Context, req *connect.Request[sockv1.RemoveMemberRequest]) (*connect.Response[sockv1.RemoveMemberResponse], error) {
	return ok(&sockv1.RemoveMemberResponse{Conversation: conversation(req.Msg.GetConversationId(), sockv1.ConversationKind_CONVERSATION_KIND_GROUP, "human-1")})
}

// Leave implements the generated handler.
func (s *Server) Leave(context.Context, *connect.Request[sockv1.LeaveRequest]) (*connect.Response[sockv1.LeaveResponse], error) {
	return ok(&sockv1.LeaveResponse{})
}

// ExportMessagingData implements the generated handler.
func (s *Server) ExportMessagingData(context.Context, *connect.Request[sockv1.ExportMessagingDataRequest]) (*connect.Response[sockv1.ExportMessagingDataResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blocked := make([]string, 0, len(s.Blocked))
	for _, b := range s.Blocked {
		blocked = append(blocked, b.GetAccountId())
	}
	return ok(&sockv1.ExportMessagingDataResponse{Conversations: s.Conversations, BlockedAccountIds: blocked})
}

// BlockAccount implements the generated handler.
func (s *Server) BlockAccount(context.Context, *connect.Request[sockv1.BlockAccountRequest]) (*connect.Response[sockv1.BlockAccountResponse], error) {
	return ok(&sockv1.BlockAccountResponse{})
}

// UnblockAccount implements the generated handler.
func (s *Server) UnblockAccount(context.Context, *connect.Request[sockv1.UnblockAccountRequest]) (*connect.Response[sockv1.UnblockAccountResponse], error) {
	return ok(&sockv1.UnblockAccountResponse{})
}

// ListBlockedAccounts implements the generated handler.
func (s *Server) ListBlockedAccounts(context.Context, *connect.Request[sockv1.ListBlockedAccountsRequest]) (*connect.Response[sockv1.ListBlockedAccountsResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ok(&sockv1.ListBlockedAccountsResponse{Blocked: s.Blocked})
}

// FileReport implements the generated handler.
func (s *Server) FileReport(context.Context, *connect.Request[sockv1.FileReportRequest]) (*connect.Response[sockv1.FileReportResponse], error) {
	return ok(&sockv1.FileReportResponse{ReportId: "report-1"})
}
