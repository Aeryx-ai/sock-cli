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
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"

	"github.com/guygrigsby/sock-cli/internal/client"
	"github.com/guygrigsby/sock-cli/internal/socktest"
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
	var a sockv1.Account
	if err := protojson.Unmarshal([]byte(out), &a); err != nil {
		t.Fatalf("not JSON: %q: %v", out, err)
	}
	if a.GetPuppet().GetName() != "build-bot" {
		t.Fatalf("account %v", &a)
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
	_, _, err := run(t, testApp(t, srv), nil, "send", "hi")
	if err != nil {
		t.Fatalf("send: %v", err)
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
	srv.SendErrs = []error{socktest.RateLimited(time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat))}
	if _, _, err := run(t, testApp(t, srv), nil, "send", "hi"); err != nil {
		t.Fatalf("send: %v", err)
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out, errOut syncBuffer
	root := NewRoot(app)
	root.SetArgs(append([]string{"listen"}, args...))
	root.SetIn(strings.NewReader(""))
	root.SetOut(&out)
	root.SetErr(&errOut)
	result := make(chan error, 1)
	go func() { result <- root.ExecuteContext(ctx) }()
	for !done() {
		select {
		case err := <-result:
			return out.String(), errOut.String(), err
		case <-ctx.Done():
			t.Fatalf("listen did not reach the expected state; stderr %q", errOut.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	return out.String(), errOut.String(), <-result
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	var waits []time.Duration
	var mu sync.Mutex
	app.Sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, d)
		return nil
	}
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
	mu.Lock()
	defer mu.Unlock()
	// First stream delivered a message: backoff resets to 1s. Second
	// delivered nothing: next wait doubles.
	if len(waits) != 2 || waits[0] != time.Second || waits[1] != 2*time.Second {
		t.Fatalf("waits %v", waits)
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
	if len(seen) != 2 || !strings.Contains(seen[1], "SOCK_MESSAGE_ID=m-ok") || !strings.Contains(seen[1], "SOCK_CONVERSATION_ID=c-1") || !strings.Contains(seen[1], "|handle-it|") {
		t.Fatalf("exec calls %v", seen)
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
