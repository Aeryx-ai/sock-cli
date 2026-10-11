package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
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
