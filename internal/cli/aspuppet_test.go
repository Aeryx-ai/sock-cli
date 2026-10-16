package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	client "github.com/guygrigsby/sock-client"
	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"

	"github.com/guygrigsby/sock-cli/internal/socktest"
)

// puppetApp is a hook's start: getenv over vars (private state and home
// added), and the config file holding body (none when body is "").
func puppetApp(t *testing.T, srv *socktest.Server, vars map[string]string, body string) *App {
	t.Helper()
	app := testApp(t, srv)
	m := map[string]string{"XDG_STATE_HOME": filepath.Join(t.TempDir(), "state"), "HOME": t.TempDir()}
	for k, v := range vars {
		m[k] = v
	}
	app.Getenv = func(k string) string { return m[k] }
	if body != "" {
		if err := os.WriteFile(app.ConfigPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return app
}

func repo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return dir
}

// notifiedAs is the bearer each NotifyOwner went out with.
func notifiedAs(srv *socktest.Server) []string {
	var out []string
	for _, c := range srv.CallsTo("NotifyOwner") {
		out = append(out, strings.TrimPrefix(c.Header.Get("Authorization"), "Bearer "))
	}
	return out
}

// sentOnly checks stdout is send's one line for text and nothing else.
func sentOnly(t *testing.T, out, text string) {
	t.Helper()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "owner-dm #") || !strings.HasSuffix(out, ": "+text+"\n") {
		t.Fatalf("stdout %q is not just send's output", out)
	}
}

const (
	loginFile  = "token = \"" + socktest.HumanToken + "\"\n"
	sharedFile = "puppet_token = \"" + socktest.Token + "\"\n"
)

func TestSendAsPuppetPrecedence(t *testing.T) {
	srv := socktest.New(t)
	url := fmt.Sprintf("url = %q\n", srv.URL)
	t.Run("SOCK_TOKEN over the file", func(t *testing.T) {
		app := puppetApp(t, srv, map[string]string{"SOCK_URL": srv.URL, "SOCK_TOKEN": "sock_pt_device-new_secret"}, url+loginFile+sharedFile)
		if _, _, err := run(t, app, strings.NewReader("hi"), "send", "--as-puppet"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("puppet_token, never the login token", func(t *testing.T) {
		app := puppetApp(t, srv, nil, url+loginFile+sharedFile)
		if _, _, err := run(t, app, strings.NewReader("hi"), "send", "--as-puppet"); err != nil {
			t.Fatal(err)
		}
	})
	got := notifiedAs(srv)
	if len(got) != 2 || got[0] != "sock_pt_device-new_secret" || got[1] != socktest.Token {
		t.Fatalf("sent as %q", got)
	}
}

// the refusal is fixed text a hook can match, and names no secret
func TestSendAsPuppetRefusesWithoutAPuppetCredential(t *testing.T) {
	srv := socktest.New(t)
	for name, body := range map[string]string{"login token only": fmt.Sprintf("url = %q\n", srv.URL) + loginFile, "no file": ""} {
		t.Run(name, func(t *testing.T) {
			app := puppetApp(t, srv, map[string]string{"SOCK_URL": srv.URL}, body)
			out, errOut, err := run(t, app, strings.NewReader("hi"), "send", "--as-puppet")
			if err == nil || err.Error() != "send --as-puppet: no puppet credential; set SOCK_TOKEN, or run sock machine-key use or sock puppets use" {
				t.Fatalf("err = %v", err)
			}
			if out != "" || errOut != "" {
				t.Fatalf("stdout %q stderr %q", out, errOut)
			}
		})
	}
	if len(srv.CallsTo("NotifyOwner")) != 0 {
		t.Fatal("sent without a puppet credential")
	}
}

// a human session in SOCK_TOKEN is not a puppet's
func TestSendAsPuppetRefusesASessionToken(t *testing.T) {
	srv := socktest.New(t)
	app := puppetApp(t, srv, map[string]string{"SOCK_URL": srv.URL, "SOCK_TOKEN": socktest.HumanToken}, "")
	_, _, err := run(t, app, strings.NewReader("hi"), "send", "--as-puppet")
	if err == nil || err.Error() != "send --as-puppet: SOCK_TOKEN is a human session, not a puppet token or machine key" {
		t.Fatalf("err = %v", err)
	}
	if len(srv.Calls) != 0 {
		t.Fatalf("called %v", srv.Calls)
	}
}

// the hook passes the directory sock-mcp starts in; in the plugin root both
// land on the shared puppet_token
func TestSendAsPuppetInThePluginRootUsesThePuppetToken(t *testing.T) {
	srv := socktest.New(t)
	root := repo(t, filepath.Join(t.TempDir(), "plugins", "sock"))
	file := fmt.Sprintf("url = %q\nmachine_key = %q\n", srv.URL, socktest.MachineKeyToken) + loginFile + sharedFile
	app := puppetApp(t, srv, map[string]string{"PLUGIN_ROOT": root}, file)
	out, errOut, err := run(t, app, strings.NewReader("turn done\n"), "send", "--as-puppet", "--project", root)
	if err != nil {
		t.Fatal(err)
	}
	sentOnly(t, out, "turn done")
	if got := notifiedAs(srv); len(got) != 1 || got[0] != socktest.Token {
		t.Fatalf("sent as %q", got)
	}
	if len(srv.CallsTo("CreatePuppet")) != 0 {
		t.Fatal("minted a puppet for the plugin root")
	}
	if !strings.Contains(errOut, "puppet_token") || strings.Contains(errOut, "sock_") {
		t.Fatalf("stderr %q", errOut)
	}
}

// --project in a repository: the machine key mints that project's puppet,
// which sends; the ensure log goes to stderr only
func TestSendAsPuppetMintsTheProjectsPuppet(t *testing.T) {
	srv := socktest.New(t)
	proj := repo(t, filepath.Join(t.TempDir(), "hookproj"))
	file := fmt.Sprintf("url = %q\nmachine_key = %q\n", srv.URL, socktest.MachineKeyToken) + loginFile + sharedFile
	app := puppetApp(t, srv, nil, file)
	out, errOut, err := run(t, app, strings.NewReader("turn done\n"), "send", "--as-puppet", "--project", proj)
	if err != nil {
		t.Fatal(err)
	}
	sentOnly(t, out, "turn done")
	creates := srv.CallsTo("CreatePuppet")
	if len(creates) != 1 || creates[0].Header.Get("Authorization") != "Bearer "+socktest.MachineKeyToken {
		t.Fatalf("creates %+v", creates)
	}
	if got := notifiedAs(srv); len(got) != 1 || got[0] != "sock_pt_device-new_secret" {
		t.Fatalf("sent as %q", got)
	}
	if !strings.Contains(errOut, "ready") || strings.Contains(errOut, "sock_") {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestSendAsPuppetFlagMisuse(t *testing.T) {
	srv := socktest.New(t)
	for want, args := range map[string][]string{
		"--project needs --as-puppet": {"send", "--project", "/tmp", "hi"},
		"drop --token":                {"send", "--as-puppet", "--token", socktest.Token, "hi"},
		"--token and --url":           {"send", "--as-puppet", "--url", srv.URL, "hi"},
	} {
		if _, _, err := run(t, puppetApp(t, srv, map[string]string{"SOCK_URL": srv.URL, "SOCK_TOKEN": socktest.Token}, ""), nil, args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v: err %v", args, err)
		}
	}
	if len(srv.Calls) != 0 {
		t.Fatalf("called %v", srv.Calls)
	}
}

// the hook sends on every turn (sock-cli-7ut): a cached project puppet token
// is used without asking the hub first, and a token the hub rejects (the
// puppet was erased) is checked, replaced and the message sent once more
func TestSendAsPuppetTrustsTheCachedTokenAndRecoversWhenItIsRejected(t *testing.T) {
	srv := socktest.New(t)
	proj := repo(t, filepath.Join(t.TempDir(), "hookproj"))
	file := fmt.Sprintf("url = %q\nmachine_key = %q\n", srv.URL, socktest.MachineKeyToken)
	state := filepath.Join(t.TempDir(), "state")
	app := puppetApp(t, srv, map[string]string{"XDG_STATE_HOME": state}, file)
	send := func(text string) {
		t.Helper()
		out, _, err := run(t, app, strings.NewReader(text), "send", "--as-puppet", "--project", proj)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(out, ": "+text+"\n") || strings.Count(out, "\n") != 1 {
			t.Fatalf("stdout %q", out)
		}
	}
	send("one")
	send("two")
	if n := len(srv.CallsTo("GetMe")); n != 0 {
		t.Fatalf("checked the cached token %d times", n)
	}
	// the puppet was erased in the app: its cached token is dead
	cached, err := filepath.Glob(filepath.Join(state, "sock-integrations", "puppets", "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	var tokenFile string
	for _, f := range cached {
		if b, err := os.ReadFile(f); err == nil && strings.TrimSpace(string(b)) == "sock_pt_device-new_secret" {
			tokenFile = f
		}
	}
	if tokenFile == "" {
		t.Fatalf("no cached token among %v", cached)
	}
	if err := os.WriteFile(tokenFile, []byte("sock_pt_gone_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	send("three")
	// the fake refuses the dead token before recording a call
	got := notifiedAs(srv)
	if want := []string{"sock_pt_device-new_secret", "sock_pt_device-new_secret", "sock_pt_device-new_secret"}; !slices.Equal(got, want) {
		t.Fatalf("sent as %q, want %q", got, want)
	}
	if n := len(srv.CallsTo("CreatePuppet")); n != 2 {
		t.Fatalf("%d creates, want a new puppet after the rejection", n)
	}
}

// a rejected token that is not a project puppet's is not retried
func TestSendAsPuppetRejectedPuppetTokenIsNotRetried(t *testing.T) {
	srv := socktest.New(t)
	app := puppetApp(t, srv, map[string]string{"SOCK_URL": srv.URL, "SOCK_TOKEN": "sock_pt_gone_secret"}, "")
	if _, _, err := run(t, app, strings.NewReader("hi"), "send", "--as-puppet"); err == nil {
		t.Fatal("sent with a rejected token")
	}
	// the fake refuses it before recording a call: count requests
	if _, _, agents, _ := srv.Snapshot(); len(agents) != 1 {
		t.Fatalf("%d requests, want one", len(agents))
	}
}

// The runner's owner gate runs `sock whoami` in the --exec child, and a Codex
// agent's sock-mcp needs the token too: a PUPPET listener hands its own
// credential to the child. A human session token never reaches it: a
// prompt-injected child could print it.
func execEnv(t *testing.T, app *App, srv *socktest.Server, args ...string) []string {
	t.Helper()
	srv.Streams = []socktest.StreamScript{{Events: []*sockv1.SubscribeResponse{socktest.Event(socktest.Text("m-1", "c-1", 1, "hi"))}, Hold: true}}
	var mu sync.Mutex
	var env []string
	app.RunExec = func(_ context.Context, _ string, _ []byte, e []string, _, _ io.Writer) error {
		mu.Lock()
		defer mu.Unlock()
		env = slices.Clone(e)
		return nil
	}
	acked := ackedCount(srv)
	if _, _, err := listenUntil(t, app, func() bool { return acked() == 1 }, append([]string{"--exec", "x"}, args...)...); err != nil {
		t.Fatalf("listen: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return env
}

func TestListenExecChildGetsAPuppetListenersCredential(t *testing.T) {
	srv := socktest.New(t)
	env := execEnv(t, testApp(t, srv), srv)
	if !slices.Contains(env, "SOCK_TOKEN="+socktest.Token) || !slices.Contains(env, "SOCK_URL="+srv.URL) {
		t.Fatalf("child env %v lacks the listener's puppet credential", env)
	}
}

func TestListenExecChildNeverGetsAHumanToken(t *testing.T) {
	srv := socktest.New(t)
	app := testApp(t, srv)
	get := app.Getenv
	app.Getenv = func(k string) string {
		if k == "SOCK_TOKEN" {
			return socktest.HumanToken
		}
		return get(k)
	}
	srv.Streams = []socktest.StreamScript{{Events: []*sockv1.SubscribeResponse{socktest.Event(socktest.Text("m-1", "c-1", 1, "hi"))}, Hold: true}}
	var env []string
	app.RunExec = func(_ context.Context, _ string, _ []byte, e []string, _, _ io.Writer) error {
		env = slices.Clone(e)
		return nil
	}
	acked := ackedCount(srv)
	if _, _, err := listenUntil(t, app, func() bool { return acked() == 1 }, "--exec", "x"); err != nil {
		t.Fatalf("listen: %v", err)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "SOCK_TOKEN=") || strings.Contains(e, socktest.HumanToken) {
			t.Fatalf("child env %v carries the human token", env)
		}
	}
}

// listen --as-puppet resolves the credential as send --as-puppet does (never
// the login) and hands that credential to the child.
func TestListenAsPuppetResolvesThePuppetAndHandsItToTheChild(t *testing.T) {
	srv := socktest.New(t)
	app := puppetApp(t, srv, map[string]string{"SOCK_URL": srv.URL, "SOCK_TOKEN": socktest.Token}, "")
	env := execEnv(t, app, srv, "--as-puppet")
	if !slices.Contains(env, "SOCK_TOKEN="+socktest.Token) || !slices.Contains(env, "SOCK_URL="+srv.URL) {
		t.Fatalf("child env %v", env)
	}
}

func TestListenAsPuppetRefusesALoginAndAMissingCredential(t *testing.T) {
	srv := socktest.New(t)
	app := puppetApp(t, srv, map[string]string{"SOCK_URL": srv.URL, "SOCK_TOKEN": socktest.HumanToken}, "")
	if _, _, err := listenUntil(t, app, func() bool { return true }, "--as-puppet", "--exec", "x"); err == nil {
		t.Fatal("a human session token was accepted as a puppet")
	}
	app = puppetApp(t, srv, map[string]string{"SOCK_URL": srv.URL}, "")
	if _, _, err := listenUntil(t, app, func() bool { return true }, "--as-puppet", "--exec", "x"); err == nil {
		t.Fatal("no credential was accepted")
	}
}

// machineKeyFile is a config file whose machine key speaks for this host.
func machineKeyFile(srv *socktest.Server) string {
	return fmt.Sprintf("url = %q\nmachine_key = %q\n", srv.URL, socktest.MachineKeyToken) + loginFile + sharedFile
}

// createdNames is the name each CreatePuppet asked for.
func createdNames(srv *socktest.Server) []string {
	var out []string
	for _, c := range srv.CallsTo("CreatePuppet") {
		out = append(out, c.Msg.(*sockv1.CreatePuppetRequest).GetName())
	}
	return out
}

// renamedTo is the name each UpdateDisplayName set, checked to go out with
// the session puppet's own token.
func renamedTo(t *testing.T, srv *socktest.Server) []string {
	t.Helper()
	var out []string
	for _, c := range srv.CallsTo("UpdateDisplayName") {
		if a := c.Header.Get("Authorization"); a != "Bearer sock_pt_device-new_secret" {
			t.Fatalf("renamed with %q", a)
		}
		out = append(out, c.Msg.(*sockv1.UpdateDisplayNameRequest).GetDisplayName())
	}
	return out
}

// noToken checks neither stream carries a token.
func noToken(t *testing.T, out, errOut string) {
	t.Helper()
	if strings.Contains(out+errOut, "sock_") || strings.Contains(out+errOut, "secret") {
		t.Fatalf("a token reached the output: stdout %q stderr %q", out, errOut)
	}
}

func TestSendAsPuppetSessionFlagMisuse(t *testing.T) {
	srv := socktest.New(t)
	for want, args := range map[string][]string{
		"--session needs --as-puppet":    {"send", "--session", "claude-code:abc", "hi"},
		"--session-name needs --session": {"send", "--as-puppet", "--session-name", "x", "hi"},
		"--project and --session":        {"send", "--as-puppet", "--project", "/tmp", "--session", "claude-code:abc", "hi"},
		"session platform must be":       {"send", "--as-puppet", "--session", "slack:abc", "hi"},
		"session id must be":             {"send", "--as-puppet", "--session", "codex:a/b", "hi"},
	} {
		if _, _, err := run(t, puppetApp(t, srv, nil, machineKeyFile(srv)), nil, args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v: err %v, want %q", args, err, want)
		}
	}
	if len(srv.Calls) != 0 {
		t.Fatalf("called %v", srv.Calls)
	}
}

// a session puppet is made with the machine key; any other credential, or
// none, is refused with what to run
func TestSendAsPuppetSessionNeedsAMachineKey(t *testing.T) {
	srv := socktest.New(t)
	url := fmt.Sprintf("url = %q\n", srv.URL)
	for name, app := range map[string]*App{
		"SOCK_TOKEN puppet token": puppetApp(t, srv, map[string]string{"SOCK_URL": srv.URL, "SOCK_TOKEN": socktest.Token}, ""),
		"SOCK_TOKEN session":      puppetApp(t, srv, map[string]string{"SOCK_URL": srv.URL, "SOCK_TOKEN": socktest.HumanToken}, ""),
		"file puppet_token":       puppetApp(t, srv, nil, url+loginFile+sharedFile),
		"none":                    puppetApp(t, srv, nil, ""),
	} {
		t.Run(name, func(t *testing.T) {
			out, errOut, err := run(t, app, strings.NewReader("hi"), "send", "--as-puppet", "--session", "claude-code:abc")
			if err == nil || !strings.Contains(err.Error(), "sock machine-key use") {
				t.Fatalf("err = %v", err)
			}
			noToken(t, out+err.Error(), errOut)
		})
	}
	if len(srv.Calls) != 0 {
		t.Fatalf("called %v", srv.Calls)
	}
}

// SOCK_TOKEN may carry the machine key too
func TestSendAsPuppetSessionTakesAMachineKeyFromSockToken(t *testing.T) {
	srv := socktest.New(t)
	app := puppetApp(t, srv, map[string]string{"SOCK_URL": srv.URL, "SOCK_TOKEN": socktest.MachineKeyToken}, "")
	out, errOut, err := run(t, app, strings.NewReader("hi"), "send", "--as-puppet", "--session", "codex:thread-1", "--session-name", "fix the build")
	if err != nil {
		t.Fatal(err)
	}
	sentOnly(t, out, "hi")
	noToken(t, out, errOut)
	if got := notifiedAs(srv); len(got) != 1 || got[0] != "sock_pt_device-new_secret" {
		t.Fatalf("sent as %q", got)
	}
}

// the hook sends on every turn: the session's puppet is made once, by the
// machine key, and every send goes out as it; with no name it is named for
// the working directory and the first 8 of the session id
func TestSendAsPuppetSessionEnsuresOnceWithTheFallbackName(t *testing.T) {
	srv := socktest.New(t)
	t.Chdir(repo(t, filepath.Join(t.TempDir(), "hookproj")))
	app := puppetApp(t, srv, nil, machineKeyFile(srv))
	for _, args := range [][]string{
		{"send", "--as-puppet", "--session", "claude-code:0123456789abcdef", "one"},
		{"send", "--as-puppet", "--session", "claude-code:0123456789abcdef", "--session-name", "  ", "two"},
	} {
		out, errOut, err := run(t, app, nil, args...)
		if err != nil {
			t.Fatal(err)
		}
		sentOnly(t, out, args[len(args)-1])
		noToken(t, out, errOut)
	}
	creates := srv.CallsTo("CreatePuppet")
	if len(creates) != 1 || creates[0].Header.Get("Authorization") != "Bearer "+socktest.MachineKeyToken {
		t.Fatalf("creates %+v", creates)
	}
	if got, want := createdNames(srv), "hookproj 01234567 on "+client.ShortHost(); got[0] != want {
		t.Fatalf("named %q, want %q", got[0], want)
	}
	if got := notifiedAs(srv); !slices.Equal(got, []string{"sock_pt_device-new_secret", "sock_pt_device-new_secret"}) {
		t.Fatalf("sent as %q", got)
	}
	if n := len(srv.CallsTo("UpdateDisplayName")); n != 0 {
		t.Fatalf("%d renames without a name", n)
	}
}

// --session-name names the new puppet, and renames it only when it changes
func TestSendAsPuppetSessionRenamesWhenTheNameChanges(t *testing.T) {
	srv := socktest.New(t)
	app := puppetApp(t, srv, nil, machineKeyFile(srv))
	for _, name := range []string{"first", "first", "second"} {
		out, errOut, err := run(t, app, strings.NewReader("hi"), "send", "--as-puppet", "--session", "codex:thread-1", "--session-name", name)
		if err != nil {
			t.Fatal(err)
		}
		sentOnly(t, out, "hi")
		noToken(t, out, errOut)
	}
	host := client.ShortHost()
	if got := createdNames(srv); !slices.Equal(got, []string{"first on " + host}) {
		t.Fatalf("created %q", got)
	}
	if got := renamedTo(t, srv); !slices.Equal(got, []string{"second on " + host}) {
		t.Fatalf("renamed to %q", got)
	}
	if n := len(notifiedAs(srv)); n != 3 {
		t.Fatalf("%d sends", n)
	}
}

// a failed rename is said on stderr and the message still goes
func TestSendAsPuppetSessionRenameFailureDoesNotFailTheSend(t *testing.T) {
	srv := socktest.New(t)
	app := puppetApp(t, srv, nil, machineKeyFile(srv))
	send := func(name string) (string, string) {
		t.Helper()
		out, errOut, err := run(t, app, strings.NewReader("hi"), "send", "--as-puppet", "--session", "codex:thread-1", "--session-name", name)
		if err != nil {
			t.Fatal(err)
		}
		sentOnly(t, out, "hi")
		noToken(t, out, errOut)
		return out, errOut
	}
	send("first")
	srv.Fail = map[string]error{"UpdateDisplayName": socktest.Error(connect.CodeInvalidArgument, sockv1.ErrorReason_ERROR_REASON_FIELD_INVALID)}
	if _, errOut := send("second"); !strings.Contains(errOut, "rename") {
		t.Fatalf("stderr %q does not say the rename failed", errOut)
	}
	if n := len(notifiedAs(srv)); n != 2 {
		t.Fatalf("%d sends", n)
	}
}
