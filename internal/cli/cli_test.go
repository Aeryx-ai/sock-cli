package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"

	"github.com/guygrigsby/sock-cli/internal/socktest"
	"github.com/guygrigsby/sock-client"
)

// run executes the root command with args and returns what it printed.
func run(t *testing.T, app *App, stdin io.Reader, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRoot(app)
	root.SetArgs(args)
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	root.SetIn(stdin)
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestVersionFlag(t *testing.T) {
	out, _, err := run(t, DefaultApp("1.2.3"), nil, "--version")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "1.2.3") {
		t.Fatalf("version output %q lacks 1.2.3", out)
	}
}

func noSleep(context.Context, time.Duration) error { return nil }

// testApp points an App at srv with no real sleeping and stdin not a TTY.
func testApp(t *testing.T, srv *socktest.Server) *App {
	t.Helper()
	r := client.DefaultRetrier()
	r.Sleep = noSleep
	return &App{
		Version: "test",
		Getenv: func(k string) string {
			return map[string]string{"SOCK_URL": srv.URL, "SOCK_TOKEN": socktest.Token}[k]
		},
		ConfigPath: filepath.Join(t.TempDir(), "absent.toml"),
		StdinIsTTY: func() bool { return false },
		Retrier:    r,
		Sleep:      noSleep,
		RunExec:    runShell,
	}
}

func TestRunShell(t *testing.T) {
	var out, errOut bytes.Buffer
	err := runShell(context.Background(), `cat; echo "$SOCK_X" >&2; exit 3`, []byte("hello"), []string{"SOCK_X=v"}, &out, &errOut)
	if out.String() != "hello" || errOut.String() != "v\n" {
		t.Fatalf("stdout %q stderr %q", out.String(), errOut.String())
	}
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("want exit status 3, got %v", err)
	}
}

func TestWhoamiText(t *testing.T) {
	srv := socktest.New(t)
	out, errOut, err := run(t, testApp(t, srv), nil, "whoami")
	if err != nil || errOut != "" {
		t.Fatalf("whoami: %v, stderr %q", err, errOut)
	}
	want := "Build Bot  puppet build-bot of human-1  id puppet-1\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestWhoamiJSON(t *testing.T) {
	srv := socktest.New(t)
	out, _, err := run(t, testApp(t, srv), nil, "--json", "whoami")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	var me sockv1.GetMeResponse
	if err := protojson.Unmarshal([]byte(out), &me); err != nil {
		t.Fatalf("not JSON: %q: %v", out, err)
	}
	if me.GetAccount().GetPuppet().GetName() != "build-bot" {
		t.Fatalf("account %v", &me)
	}
}

func TestBadTokenIsActionable(t *testing.T) {
	srv := socktest.New(t)
	app := testApp(t, srv)
	app.Getenv = func(k string) string {
		return map[string]string{"SOCK_URL": srv.URL, "SOCK_TOKEN": "sock_pt_nope"}[k]
	}
	_, _, err := run(t, app, nil, "whoami")
	if err == nil || !strings.Contains(err.Error(), "rejected the token") {
		t.Fatalf("got %v", err)
	}
}

func TestSendNotifiesOwnerFromArgs(t *testing.T) {
	srv := socktest.New(t)
	out, _, err := run(t, testApp(t, srv), nil, "send", "build", "done", "✅")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	sent, _, _, _ := srv.Snapshot()
	if len(sent) != 1 || sent[0].ConversationID != "" || sent[0].Text != "build done ✅" {
		t.Fatalf("sent %+v", sent)
	}
	if len(sent[0].IdempotencyKey) == 0 || len(sent[0].IdempotencyKey) > 128 {
		t.Fatalf("idempotency key %q", sent[0].IdempotencyKey)
	}
	if !strings.Contains(out, "owner-dm #1") {
		t.Fatalf("output %q", out)
	}
}

func TestSendToConversationFromStdin(t *testing.T) {
	srv := socktest.New(t)
	_, _, err := run(t, testApp(t, srv), strings.NewReader("line one\nline two\n"), "send", "--conversation", "c-9")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	sent, _, _, _ := srv.Snapshot()
	if len(sent) != 1 || sent[0].ConversationID != "c-9" || sent[0].Text != "line one\nline two" {
		t.Fatalf("sent %+v", sent)
	}
}

func TestSendRetriesWithSameKey(t *testing.T) {
	srv := socktest.New(t)
	srv.SendErrs = []error{socktest.Unavailable(), socktest.RateLimited("1")}
	app := testApp(t, srv)
	app.Retrier.Base = 2 * time.Second // so the computed backoff cannot pass for Retry-After
	waits := recordSleeps(&app.Retrier.Sleep)
	_, _, err := run(t, app, nil, "send", "hi")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// Backoff for Unavailable, then the server's Retry-After of 1s.
	if w := waits(); !slices.Equal(w, []time.Duration{2 * time.Second, time.Second}) {
		t.Fatalf("waits %v, want [2s 1s]", w)
	}
	sent, _, _, _ := srv.Snapshot()
	if len(sent) != 3 {
		t.Fatalf("want 3 attempts, got %d", len(sent))
	}
	for _, s := range sent {
		if s.IdempotencyKey != sent[0].IdempotencyKey {
			t.Fatalf("keys differ across retries: %+v", sent)
		}
	}
}

func TestSendDistinctInvocationsGetDistinctKeys(t *testing.T) {
	srv := socktest.New(t)
	for range 2 {
		if _, _, err := run(t, testApp(t, srv), nil, "send", "hi"); err != nil {
			t.Fatal(err)
		}
	}
	sent, _, _, _ := srv.Snapshot()
	if sent[0].IdempotencyKey == sent[1].IdempotencyKey {
		t.Fatal("two invocations reused one key")
	}
}

func TestSendPermanentErrorNotRetried(t *testing.T) {
	srv := socktest.New(t)
	srv.SendErrs = []error{socktest.NotMember()}
	_, _, err := run(t, testApp(t, srv), nil, "send", "--conversation", "c-1", "hi")
	if err == nil || !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("got %v", err)
	}
	sent, _, _, _ := srv.Snapshot()
	if len(sent) != 1 {
		t.Fatalf("retried a permanent error: %d calls", len(sent))
	}
}

func TestSendInputValidation(t *testing.T) {
	cases := []struct {
		name    string
		stdin   string
		tty     bool
		args    []string
		wantErr string
	}{
		{"tty no args", "", true, nil, "no text"},
		{"empty pipe", "", false, nil, "empty"},
		{"only newline", "\n", false, nil, "empty"},
		{"too long", strings.Repeat("a", 65537), false, nil, "65536"},
		{"invalid utf8", "\xff\xfe", false, nil, "UTF-8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := socktest.New(t)
			app := testApp(t, srv)
			app.StdinIsTTY = func() bool { return c.tty }
			_, _, err := run(t, app, strings.NewReader(c.stdin), append([]string{"send"}, c.args...)...)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
			sent, _, _, _ := srv.Snapshot()
			if len(sent) != 0 {
				t.Fatal("invalid input reached the server")
			}
		})
	}
}

func TestSendExactlyMaxBytesAllowed(t *testing.T) {
	srv := socktest.New(t)
	if _, _, err := run(t, testApp(t, srv), strings.NewReader(strings.Repeat("a", 65536)), "send"); err != nil {
		t.Fatalf("65536 bytes refused: %v", err)
	}
}

func TestSendRetriesWithSameKeyOnHTTPDateRetryAfter(t *testing.T) {
	srv := socktest.New(t)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	srv.SendErrs = []error{socktest.RateLimited(now.Add(2 * time.Second).Format(http.TimeFormat))}
	app := testApp(t, srv)
	app.Retrier.Now = func() time.Time { return now }
	waits := recordSleeps(&app.Retrier.Sleep)
	if _, _, err := run(t, app, nil, "send", "hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if w := waits(); len(w) != 1 || w[0] < time.Second || w[0] > 2*time.Second {
		t.Fatalf("waits %v, want one wait between 1s and 2s", w)
	}
	sent, _, _, _ := srv.Snapshot()
	if len(sent) != 2 || sent[0].IdempotencyKey != sent[1].IdempotencyKey {
		t.Fatalf("want 2 attempts on one key, got %+v", sent)
	}
}

func TestFetchDrainsAndAcks(t *testing.T) {
	srv := socktest.New(t)
	for i := range 150 {
		srv.Pending = append(srv.Pending, socktest.Text(fmt.Sprintf("m-%d", i), "c-1", int64(i+1), "hello"))
	}
	out, _, err := run(t, testApp(t, srv), nil, "fetch")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if n := strings.Count(out, "\n"); n != 150 {
		t.Fatalf("printed %d lines, want 150", n)
	}
	_, acked, _, _ := srv.Snapshot()
	if len(acked) != 150 {
		t.Fatalf("acked %d, want 150", len(acked))
	}
}

func TestFetchStopsWhenAckedMessagesComeBack(t *testing.T) {
	srv := socktest.New(t)
	srv.KeepAcked = true
	srv.Pending = []*sockv1.Message{socktest.Text("m-1", "c-1", 1, "a"), socktest.Text("m-2", "c-1", 2, "b")}
	out, _, err := run(t, testApp(t, srv), nil, "fetch")
	if err == nil || !strings.Contains(err.Error(), "already printed") {
		t.Fatalf("want a no-progress error, got %v", err)
	}
	if strings.Count(out, "\n") != 2 {
		t.Fatalf("output %q", out)
	}
}

func TestFetchNoAckPrintsOneBatchAndLeavesPending(t *testing.T) {
	srv := socktest.New(t)
	srv.Pending = []*sockv1.Message{socktest.Text("m-1", "c-1", 1, "a"), socktest.Text("m-2", "c-1", 2, "b")}
	out, _, err := run(t, testApp(t, srv), nil, "fetch", "--no-ack")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if strings.Count(out, "\n") != 2 {
		t.Fatalf("output %q", out)
	}
	_, acked, _, _ := srv.Snapshot()
	if len(acked) != 0 {
		t.Fatalf("acked %v with --no-ack", acked)
	}
}

func TestFetchEmpty(t *testing.T) {
	srv := socktest.New(t)
	out, _, err := run(t, testApp(t, srv), nil, "fetch")
	if err != nil || out != "" {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestFetchJSONLines(t *testing.T) {
	srv := socktest.New(t)
	srv.Pending = []*sockv1.Message{socktest.Text("m-1", "c-1", 1, "multi\nline")}
	out, _, err := run(t, testApp(t, srv), nil, "--json", "fetch")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("a multi-line message must stay one JSON line: %q", out)
	}
	var m sockv1.Message
	if err := protojson.Unmarshal([]byte(lines[0]), &m); err != nil || m.GetText() != "multi\nline" {
		t.Fatalf("decode %q: %v", lines[0], err)
	}
}

func TestConversations(t *testing.T) {
	srv := socktest.New(t)
	srv.Conversations = []*sockv1.Conversation{
		{Id: "c-1", Kind: sockv1.ConversationKind_CONVERSATION_KIND_DIRECT, MemberIds: []string{"human-1", "puppet-1"}},
		{Id: "c-2", Kind: sockv1.ConversationKind_CONVERSATION_KIND_GROUP, MemberIds: []string{"human-1", "human-2", "puppet-1"}},
	}
	out, _, err := run(t, testApp(t, srv), nil, "conversations")
	if err != nil {
		t.Fatal(err)
	}
	want := "c-1  direct  members human-1,puppet-1\nc-2  group  members human-1,human-2,puppet-1\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

// listenUntil runs listen until done reports true, then cancels it.
func listenUntil(t *testing.T, app *App, done func() bool, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut syncBuffer
	err := listenUntilTo(t, app, &out, &errOut, done, args...)
	return out.String(), errOut.String(), err
}

// listenUntilTo is listenUntil with caller-supplied output buffers.
func listenUntilTo(t *testing.T, app *App, out, errOut *syncBuffer, done func() bool, args ...string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := NewRoot(app)
	root.SetArgs(append([]string{"listen"}, args...))
	root.SetIn(strings.NewReader(""))
	root.SetOut(out)
	root.SetErr(errOut)
	result := make(chan error, 1)
	go func() { result <- root.ExecuteContext(ctx) }()
	for !done() {
		select {
		case err := <-result:
			return err
		case <-ctx.Done():
			t.Fatalf("listen did not reach the expected state; stderr %q", errOut.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	return <-result // wait for listen to exit before reading its output
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
	// onWrite runs before each write, so a test can observe server state at
	// the moment output is produced.
	onWrite func()
	// err, when set, fails every write.
	err error
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	if s.onWrite != nil {
		s.onWrite()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func ackedCount(srv *socktest.Server) func() int {
	return func() int {
		_, acked, _, _ := srv.Snapshot()
		return len(acked)
	}
}

func TestListenPrintsAcksAndIgnoresHeartbeats(t *testing.T) {
	srv := socktest.New(t)
	srv.Streams = []socktest.StreamScript{{
		Events: []*sockv1.SubscribeResponse{
			socktest.Event(socktest.Text("m-1", "c-1", 1, "first")),
			socktest.Heartbeat(),
			socktest.Event(socktest.Text("m-2", "c-1", 2, "second")),
		},
		Hold: true,
	}}
	acked := ackedCount(srv)
	out, _, err := listenUntil(t, testApp(t, srv), func() bool { return acked() == 2 })
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if strings.Count(out, "\n") != 2 || !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("output %q", out)
	}
}

func TestListenReconnectsAfterServerDrop(t *testing.T) {
	srv := socktest.New(t)
	srv.Streams = []socktest.StreamScript{
		{Events: []*sockv1.SubscribeResponse{socktest.Event(socktest.Text("m-1", "c-1", 1, "before"))}, End: socktest.Unavailable()},
		{End: nil}, // clean close also reconnects
		{Events: []*sockv1.SubscribeResponse{socktest.Event(socktest.Text("m-2", "c-1", 2, "after"))}, Hold: true},
	}
	app := testApp(t, srv)
	waits := recordSleeps(&app.Sleep)
	acked := ackedCount(srv)
	out, errOut, err := listenUntil(t, app, func() bool { return acked() == 2 })
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Fatalf("output %q", out)
	}
	if !strings.Contains(errOut, "reconnecting") {
		t.Fatalf("stderr %q", errOut)
	}
	_, _, _, subs := srv.Snapshot()
	if subs != 3 {
		t.Fatalf("subscribes %d, want 3", subs)
	}
	// First stream received a message: backoff resets to 1s. Second
	// received nothing: next wait doubles.
	if w := waits(); len(w) != 2 || w[0] != time.Second || w[1] != 2*time.Second {
		t.Fatalf("waits %v", w)
	}
}

// recordSleeps replaces *sleep with a no-op that records each duration and
// returns a reader for them.
func recordSleeps(sleep *func(context.Context, time.Duration) error) func() []time.Duration {
	var mu sync.Mutex
	var waits []time.Duration
	*sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, d)
		return nil
	}
	return func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(waits)
	}
}

// A connection that received only heartbeats was healthy; the next failure
// starts the backoff over.
func TestListenHeartbeatResetsBackoff(t *testing.T) {
	srv := socktest.New(t)
	srv.Streams = []socktest.StreamScript{
		{Events: []*sockv1.SubscribeResponse{socktest.Heartbeat()}, End: socktest.Unavailable()},
		{Events: []*sockv1.SubscribeResponse{socktest.Heartbeat()}, End: socktest.Unavailable()},
	}
	app := testApp(t, srv)
	waits := recordSleeps(&app.Sleep)
	_, _, err := listenUntil(t, app, func() bool { return subscribes(srv) >= 3 })
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if w := waits(); len(w) != 2 || w[0] != time.Second || w[1] != time.Second {
		t.Fatalf("waits %v, want [1s 1s]", w)
	}
}

// Jitter shortens the backoff only; the cap and Retry-After still bound the
// final wait.
func TestListenAppliesJitterToBackoff(t *testing.T) {
	srv := socktest.New(t)
	srv.Streams = []socktest.StreamScript{
		{End: socktest.Unavailable()},
		{End: socktest.Unavailable()},
		{End: socktest.RateLimited("10")},
	}
	app := testApp(t, srv)
	app.Jitter = func(d time.Duration) time.Duration { return d / 2 }
	waits := recordSleeps(&app.Sleep)
	_, _, err := listenUntil(t, app, func() bool { return subscribes(srv) >= 4 })
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 10 * time.Second}
	if w := waits(); !slices.Equal(w, want) {
		t.Fatalf("waits %v, want %v", w, want)
	}
}

func TestEqualJitterBounds(t *testing.T) {
	for range 1000 {
		d := equalJitter(time.Minute)
		if d < 30*time.Second || d > time.Minute {
			t.Fatalf("equalJitter(1m) = %v, want [30s, 1m]", d)
		}
	}
}

func subscribes(srv *socktest.Server) int {
	_, _, _, n := srv.Snapshot()
	return n
}

// A half-open connection delivers nothing and never errors; the watchdog
// must notice the silence and reconnect.
func TestListenReconnectsWhenStreamGoesSilent(t *testing.T) {
	srv := socktest.New(t) // no scripts: every Subscribe holds silently
	app := testApp(t, srv)
	app.ListenIdle = 50 * time.Millisecond
	_, errOut, err := listenUntil(t, app, func() bool { return subscribes(srv) >= 2 })
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if !strings.Contains(errOut, "no message or heartbeat in 50ms") {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestListenHeartbeatsKeepStreamAlive(t *testing.T) {
	srv := socktest.New(t)
	srv.Streams = []socktest.StreamScript{{Hold: true, HeartbeatEvery: 20 * time.Millisecond}}
	app := testApp(t, srv)
	app.ListenIdle = 200 * time.Millisecond
	start := time.Now()
	_, errOut, err := listenUntil(t, app, func() bool { return time.Since(start) > 700*time.Millisecond })
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if n := subscribes(srv); n != 1 {
		t.Fatalf("subscribes %d, want 1; stderr %q", n, errOut)
	}
}

func TestListenReconnectHonorsRetryAfter(t *testing.T) {
	srv := socktest.New(t)
	srv.Streams = []socktest.StreamScript{
		{End: socktest.RateLimited("5")},
		{End: socktest.RateLimited("600")}, // above reconnectMax
		{End: socktest.RateLimited("0")},   // below the backoff
	}
	app := testApp(t, srv)
	waits := recordSleeps(&app.Sleep)
	_, _, err := listenUntil(t, app, func() bool { return subscribes(srv) >= 4 })
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	want := []time.Duration{5 * time.Second, time.Minute, 4 * time.Second}
	if w := waits(); !slices.Equal(w, want) {
		t.Fatalf("waits %v, want %v", w, want)
	}
}

func TestListenStopsOnBadToken(t *testing.T) {
	srv := socktest.New(t)
	app := testApp(t, srv)
	app.Getenv = func(k string) string {
		return map[string]string{"SOCK_URL": srv.URL, "SOCK_TOKEN": "sock_pt_nope"}[k]
	}
	_, _, err := listenUntil(t, app, func() bool { return false })
	if err == nil || !strings.Contains(err.Error(), "rejected the token") {
		t.Fatalf("want token error, got %v", err)
	}
}

// A wrong URL path is a 404, which Connect reports as Unimplemented. It
// never fixes itself, so listen must exit instead of retrying forever.
func TestListenStopsOnWrongURL(t *testing.T) {
	srv := socktest.New(t)
	app := testApp(t, srv)
	app.Getenv = func(k string) string {
		return map[string]string{"SOCK_URL": srv.URL + "/not-sock", "SOCK_TOKEN": socktest.Token}[k]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	root := NewRoot(app)
	root.SetArgs([]string{"listen"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.ExecuteContext(ctx)
	if ctx.Err() != nil {
		t.Fatal("listen kept retrying a wrong URL")
	}
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want a 404 error for a wrong URL, got %v", err)
	}
}

// A failed --exec is retried on the same stream with backoff; it does not
// wait for a reconnect that a healthy stream never needs.
func TestListenExecRetriesInProcess(t *testing.T) {
	srv := socktest.New(t)
	srv.Streams = []socktest.StreamScript{{
		Events: []*sockv1.SubscribeResponse{socktest.Event(socktest.Text("m-1", "c-1", 1, "flaky"))},
		Hold:   true,
	}}
	app := testApp(t, srv)
	waits := recordSleeps(&app.Sleep)
	var calls atomic.Int32
	app.RunExec = func(context.Context, string, []byte, []string, io.Writer, io.Writer) error {
		if calls.Add(1) < 3 {
			return errors.New("exit status 1")
		}
		return nil
	}
	acked := ackedCount(srv)
	_, errOut, err := listenUntil(t, app, func() bool { return acked() == 1 }, "--exec", "handle-it")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("exec ran %d times, want 3", calls.Load())
	}
	if _, _, _, subs := srv.Snapshot(); subs != 1 {
		t.Fatalf("subscribes %d, want 1: the retry must not need a reconnect", subs)
	}
	if w := waits(); len(w) != 2 || w[0] != time.Second || w[1] != 2*time.Second {
		t.Fatalf("waits %v", w)
	}
	if !strings.Contains(errOut, "retrying") {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestListenExecAcksOnlyOnSuccess(t *testing.T) {
	srv := socktest.New(t)
	srv.Streams = []socktest.StreamScript{{
		Events: []*sockv1.SubscribeResponse{
			socktest.Event(socktest.Text("m-fail", "c-1", 1, "bad")),
			socktest.Event(socktest.Text("m-ok", "c-1", 2, "good")),
		},
		Hold: true,
	}}
	app := testApp(t, srv)
	var mu sync.Mutex
	var seen []string
	app.RunExec = func(_ context.Context, cmdline string, stdin []byte, env []string, _, _ io.Writer) error {
		mu.Lock()
		defer mu.Unlock()
		var m sockv1.Message
		if err := protojson.Unmarshal(stdin, &m); err != nil {
			return err
		}
		seen = append(seen, m.GetId()+"|"+cmdline+"|"+strings.Join(env, ","))
		if m.GetId() == "m-fail" {
			return errors.New("exit status 1")
		}
		return nil
	}
	recordSleeps(&app.Sleep)
	acked := ackedCount(srv)
	_, errOut, err := listenUntil(t, app, func() bool { return acked() == 1 }, "--exec", "handle-it")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, ackedIDs, _, _ := srv.Snapshot()
	if len(ackedIDs) != 1 || ackedIDs[0] != "m-ok" {
		t.Fatalf("acked %v, want only m-ok", ackedIDs)
	}
	if !strings.Contains(errOut, "m-fail") || !strings.Contains(errOut, "not acked") {
		t.Fatalf("stderr %q", errOut)
	}
	mu.Lock()
	defer mu.Unlock()
	// m-fail is attempted execAttempts times, then m-ok runs once
	if last := seen[len(seen)-1]; len(seen) != execAttempts+1 || !strings.Contains(last, "SOCK_MESSAGE_ID=m-ok") || !strings.Contains(last, "SOCK_CONVERSATION_ID=c-1") || !strings.Contains(last, "|handle-it|") {
		t.Fatalf("exec calls %v", seen)
	}
}

// Ctrl-C while a handler runs makes the ack fail with a canceled context.
// That is a clean exit, not something to report.
func TestListenCtrlCDuringHandlerLogsNothing(t *testing.T) {
	srv := socktest.New(t)
	srv.Streams = []socktest.StreamScript{{
		Events: []*sockv1.SubscribeResponse{socktest.Event(socktest.Text("m-1", "c-1", 1, "hi"))},
		Hold:   true,
	}}
	app := testApp(t, srv)
	var started atomic.Bool
	app.RunExec = func(ctx context.Context, _ string, _ []byte, _ []string, _, _ io.Writer) error {
		started.Store(true)
		<-ctx.Done()
		return nil // handler finished; only the ack sees the cancel
	}
	_, errOut, err := listenUntil(t, app, started.Load, "--exec", "handle-it")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if errOut != "" {
		t.Fatalf("stderr %q, want nothing on Ctrl-C", errOut)
	}
}

// Message text reaches --exec only as stdin; shell metacharacters in it must
// never execute, and env values carry only ids.
func TestListenExecNeverInterpolatesMessage(t *testing.T) {
	srv := socktest.New(t)
	marker := filepath.Join(t.TempDir(), "pwned")
	evil := "$(touch " + marker + "); `touch " + marker + "`; touch " + marker
	srv.Streams = []socktest.StreamScript{{
		Events: []*sockv1.SubscribeResponse{socktest.Event(socktest.Text("m-1", "c-1", 1, evil))},
		Hold:   true,
	}}
	app := testApp(t, srv) // real runShell
	got := filepath.Join(t.TempDir(), "stdin")
	acked := ackedCount(srv)
	_, _, err := listenUntil(t, app, func() bool { return acked() == 1 }, "--exec", "cat > "+got)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("message text was executed by the shell")
	}
	b, err := os.ReadFile(got)
	if err != nil || !strings.Contains(string(b), "m-1") || !strings.Contains(string(b), "touch") {
		t.Fatalf("stdin %q err %v", b, err)
	}
}

func TestReconnectable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"clean close", nil, true},
		{"plain error", errors.New("connection reset"), true},
		{"idle watchdog", errStreamIdle, true},
		{"unavailable", connect.NewError(connect.CodeUnavailable, errors.New("x")), true},
		{"resource exhausted", connect.NewError(connect.CodeResourceExhausted, errors.New("x")), true},
		{"internal", connect.NewError(connect.CodeInternal, errors.New("x")), true},
		{"unauthenticated", connect.NewError(connect.CodeUnauthenticated, errors.New("x")), false},
		{"permission denied", connect.NewError(connect.CodePermissionDenied, errors.New("x")), false},
		{"invalid argument", connect.NewError(connect.CodeInvalidArgument, errors.New("x")), false},
		{"unimplemented", connect.NewError(connect.CodeUnimplemented, errors.New("x")), false},
		{"wrapped terminal", fmt.Errorf("wrap: %w", connect.NewError(connect.CodeUnauthenticated, errors.New("x"))), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reconnectable(c.err); got != c.want {
				t.Fatalf("reconnectable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestListenBackoffCapsAtReconnectMax(t *testing.T) {
	srv := socktest.New(t)
	for range 7 {
		srv.Streams = append(srv.Streams, socktest.StreamScript{End: socktest.Unavailable()})
	}
	app := testApp(t, srv)
	waits := recordSleeps(&app.Sleep)
	_, _, err := listenUntil(t, app, func() bool { return subscribes(srv) >= 8 })
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute}
	if w := waits(); len(w) < len(want) || !slices.Equal(w[:len(want)], want) {
		t.Fatalf("waits %v, want prefix %v", w, want)
	}
	for _, w := range waits() {
		if w > time.Minute {
			t.Fatalf("wait %v exceeds the 1m cap", w)
		}
	}
}

// ackedWhenWritten returns a buffer that records how many acks the server
// had seen each time output was written.
func ackedWhenWritten(srv *socktest.Server) (*syncBuffer, func() []int) {
	var mu sync.Mutex
	var seen []int
	buf := &syncBuffer{onWrite: func() {
		_, acked, _, _ := srv.Snapshot()
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, len(acked))
	}}
	return buf, func() []int {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

func TestListenAcksOnlyAfterPrinting(t *testing.T) {
	srv := socktest.New(t)
	srv.Streams = []socktest.StreamScript{{
		Events: []*sockv1.SubscribeResponse{
			socktest.Event(socktest.Text("m-1", "c-1", 1, "a")),
			socktest.Event(socktest.Text("m-2", "c-1", 2, "b")),
		},
		Hold: true,
	}}
	out, seen := ackedWhenWritten(srv)
	var errOut syncBuffer
	acked := ackedCount(srv)
	if err := listenUntilTo(t, testApp(t, srv), out, &errOut, func() bool { return acked() == 2 }); err != nil {
		t.Fatalf("listen: %v", err)
	}
	// Message n is printed while only the n-1 earlier ones are acked.
	if got := seen(); !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("acks seen at each print %v, want [0 1]", got)
	}
}

func TestFetchAcksOnlyAfterPrintingBatch(t *testing.T) {
	srv := socktest.New(t)
	for i := range 150 {
		srv.Pending = append(srv.Pending, socktest.Text(fmt.Sprintf("m-%d", i), "c-1", int64(i+1), "hello"))
	}
	out, seen := ackedWhenWritten(srv)
	if err := runTo(t, testApp(t, srv), out, "fetch"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got := seen()
	if len(got) != 150 {
		t.Fatalf("%d prints, want 150", len(got))
	}
	for i, n := range got {
		if want := 100 * (i / 100); n != want {
			t.Fatalf("print %d saw %d acks, want %d (batch acked only after its prints)", i, n, want)
		}
	}
}

func TestFetchNoAckMultiBatchPrintsOnlyOneBatch(t *testing.T) {
	srv := socktest.New(t)
	for i := range fetchBatch + 50 {
		srv.Pending = append(srv.Pending, socktest.Text(fmt.Sprintf("m-%d", i), "c-1", int64(i+1), "hello"))
	}
	out, _, err := run(t, testApp(t, srv), nil, "fetch", "--no-ack")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if n := strings.Count(out, "\n"); n != fetchBatch {
		t.Fatalf("printed %d lines, want one batch of %d", n, fetchBatch)
	}
	if _, acked, _, _ := srv.Snapshot(); len(acked) != 0 {
		t.Fatalf("acked %v with --no-ack", acked)
	}
}

// runTo executes the root command with stdout going to out.
func runTo(t *testing.T, app *App, out io.Writer, args ...string) error {
	t.Helper()
	root := NewRoot(app)
	root.SetArgs(args)
	root.SetIn(strings.NewReader(""))
	root.SetOut(out)
	root.SetErr(io.Discard)
	return root.ExecuteContext(context.Background())
}

func TestFetchPrintFailureLeavesMessagesUnacked(t *testing.T) {
	srv := socktest.New(t)
	srv.Pending = []*sockv1.Message{socktest.Text("m-1", "c-1", 1, "a"), socktest.Text("m-2", "c-1", 2, "b")}
	err := runTo(t, testApp(t, srv), &syncBuffer{err: errors.New("broken pipe")}, "fetch")
	if err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("want the print error, got %v", err)
	}
	if _, acked, _, _ := srv.Snapshot(); len(acked) != 0 {
		t.Fatalf("acked %v after a print failure", acked)
	}
}

func TestFetchAckFailureLeavesMessagesPending(t *testing.T) {
	srv := socktest.New(t)
	srv.Pending = []*sockv1.Message{socktest.Text("m-1", "c-1", 1, "a")}
	srv.AckErrs = []error{socktest.NotMember()} // permanent: not retried
	var out syncBuffer
	if err := runTo(t, testApp(t, srv), &out, "fetch"); err == nil {
		t.Fatal("want an error when the ack fails")
	}
	if _, acked, _, _ := srv.Snapshot(); len(acked) != 0 {
		t.Fatalf("acked %v after an ack failure", acked)
	}
	srv.AckErrs = nil
	if err := runTo(t, testApp(t, srv), &out, "fetch"); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if strings.Count(out.String(), " #1 ") != 2 {
		t.Fatalf("message not redelivered after the failed ack: %q", out.String())
	}
}

func TestListenPrintFailureLeavesMessageUnacked(t *testing.T) {
	srv := socktest.New(t)
	srv.Streams = []socktest.StreamScript{{
		Events: []*sockv1.SubscribeResponse{socktest.Event(socktest.Text("m-1", "c-1", 1, "a"))},
		Hold:   true,
	}}
	out := &syncBuffer{err: errors.New("broken pipe")}
	var errOut syncBuffer
	done := func() bool { return strings.Contains(errOut.String(), "not acked") }
	if err := listenUntilTo(t, testApp(t, srv), out, &errOut, done); err != nil {
		t.Fatalf("listen: %v", err)
	}
	if _, acked, _, _ := srv.Snapshot(); len(acked) != 0 {
		t.Fatalf("acked %v after a print failure", acked)
	}
	if !strings.Contains(errOut.String(), "m-1") || !strings.Contains(errOut.String(), "broken pipe") {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestListenAckFailureLogsAndKeepsStreaming(t *testing.T) {
	srv := socktest.New(t)
	srv.AckErrs = []error{socktest.NotMember()}
	srv.Streams = []socktest.StreamScript{{
		Events: []*sockv1.SubscribeResponse{
			socktest.Event(socktest.Text("m-1", "c-1", 1, "a")),
			socktest.Event(socktest.Text("m-2", "c-1", 2, "b")),
		},
		Hold: true,
	}}
	acked := ackedCount(srv)
	_, errOut, err := listenUntil(t, testApp(t, srv), func() bool { return acked() == 1 })
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if _, ids, _, _ := srv.Snapshot(); len(ids) != 1 || ids[0] != "m-2" {
		t.Fatalf("acked %v, want only m-2", ids)
	}
	if !strings.Contains(errOut, "message m-1") {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestSendSaysItGaveUpAfterTheAttempts(t *testing.T) {
	srv := socktest.New(t)
	for range 6 {
		srv.SendErrs = append(srv.SendErrs, socktest.Unavailable())
	}
	app := testApp(t, srv)
	_, _, err := run(t, app, nil, "send", "hi")
	if err == nil || !strings.Contains(err.Error(), "gave up after 6 attempts") {
		t.Fatalf("err %v, want it to say it gave up after 6 attempts", err)
	}
}

func TestSendRetryBudgetBoundsRetryAfter(t *testing.T) {
	srv := socktest.New(t)
	for range 6 {
		srv.SendErrs = append(srv.SendErrs, socktest.RateLimited("3600"))
	}
	app := testApp(t, srv)
	app.RetryBudget = 50 * time.Millisecond
	app.Retrier.Sleep = client.SleepCtx // really waits, so only the budget ends it
	start := time.Now()
	_, _, err := run(t, app, nil, "send", "hi")
	if time.Since(start) > 5*time.Second {
		t.Fatalf("waited %s past a 50ms budget", time.Since(start))
	}
	if err == nil || !strings.Contains(err.Error(), "gave up after 50ms") {
		t.Fatalf("err %v, want it to say it gave up after 50ms", err)
	}
}
