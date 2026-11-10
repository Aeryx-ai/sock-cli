package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"

	"github.com/aeryx-ai/sock-cli/internal/config"
	"github.com/aeryx-ai/sock-cli/internal/socktest"
	client "github.com/aeryx-ai/sock-client"
)

// humanApp points an App at srv with no token anywhere and a config file
// path that does not exist yet. env adds environment variables.
func humanApp(t *testing.T, srv *socktest.Server, env map[string]string) *App {
	t.Helper()
	r := client.DefaultRetrier()
	r.Sleep = noSleep
	vars := map[string]string{"SOCK_URL": srv.URL}
	for k, v := range env {
		vars[k] = v
	}
	return &App{
		Version:      "test",
		Getenv:       func(k string) string { return vars[k] },
		ConfigPath:   filepath.Join(t.TempDir(), "sock", "config.toml"),
		StdinIsTTY:   func() bool { return false },
		Retrier:      r,
		Sleep:        noSleep,
		RunExec:      runShell,
		Hostname:     func() (string, error) { return "box", nil },
		LoginTimeout: 10 * time.Second,
		OpenBrowser: func(string) error {
			t.Error("browser opened")
			return errors.New("unexpected")
		},
	}
}

// browser records what login opened: the hub's page with the code filled
// in. Nothing happens in it; the fake hub answers the terminal directly.
type browser struct {
	t      *testing.T
	hub    string
	opened []string
}

func (b *browser) open(raw string) error {
	b.opened = append(b.opened, raw)
	if raw != b.hub+"/login?code="+socktest.UserCode {
		b.t.Errorf("opened %q, want the hub's /login with the code", raw)
	}
	return nil
}

func withBrowser(t *testing.T, app *App, srv *socktest.Server) *browser {
	b := &browser{t: t, hub: srv.URL}
	app.OpenBrowser = b.open
	return b
}

func writeConfig(t *testing.T, app *App, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(app.ConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fileToken(t *testing.T, app *App) string {
	t.Helper()
	tok, err := config.FileToken(app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestLoginRefusesExistingToken(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	writeConfig(t, app, "token = \"sock_pt_keepme_secret\"\n")
	_, _, err := run(t, app, nil, "login")
	if err == nil || !strings.Contains(err.Error(), "already holds a token") {
		t.Fatalf("got %v", err)
	}
	if fileToken(t, app) != "sock_pt_keepme_secret" || len(srv.CallsTo("StartTerminalSignIn")) != 0 {
		t.Fatal("existing token touched")
	}
}

func TestLoginWarnsWhenEnvTokenSet(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, map[string]string{"SOCK_TOKEN": socktest.Token})
	withBrowser(t, app, srv)
	_, errOut, err := run(t, app, nil, "login")
	if err != nil || !strings.Contains(errOut, "SOCK_TOKEN") {
		t.Fatalf("err %v, stderr %q", err, errOut)
	}
	for _, m := range []string{"StartTerminalSignIn", "FinishTerminalSignIn"} {
		if h := srv.CallsTo(m)[0].Header.Get("Authorization"); h != "" {
			t.Fatalf("SOCK_TOKEN sent on %s: %q", m, h)
		}
	}
}

func TestLoginJSONClearsToken(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	out, _, err := run(t, app, nil, "--json", "login")
	if err != nil {
		t.Fatal(err)
	}
	var s sockv1.Session
	if err := protojson.Unmarshal([]byte(out), &s); err != nil || s.GetToken() != "" || s.GetDevice().GetId() != socktest.HumanDeviceID {
		t.Fatalf("json %q: %v", out, err)
	}
}

func TestLogout(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	writeConfig(t, app, fmt.Sprintf("url = %q\ntoken = %q\n", srv.URL, socktest.HumanToken))
	out, _, err := run(t, app, nil, "logout")
	if err != nil || out != "signed out\n" {
		t.Fatalf("logout: %q, %v", out, err)
	}
	calls := srv.CallsTo("SignOut")
	if len(calls) != 1 || calls[0].Header.Get("Authorization") != "Bearer "+socktest.HumanToken {
		t.Fatalf("SignOut calls %v", calls)
	}
	cfg, err := config.Load(config.Sources{Anonymous: true, Getenv: func(string) string { return "" }, Path: app.ConfigPath})
	if err != nil || cfg.Token != "" || cfg.URL != srv.URL {
		t.Fatalf("config after logout %+v, %v", cfg, err)
	}
}

// logout keeps the machine key, and says what that leaves on.
func TestLogoutSaysTheMachineKeyStays(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	writeConfig(t, app, fmt.Sprintf("url = %q\ntoken = %q\nmachine_key = %q\n", srv.URL, socktest.HumanToken, socktest.MachineKeyToken))
	out, errOut, err := run(t, app, nil, "logout")
	if err != nil || out != "signed out\n" {
		t.Fatalf("logout: %q, %v", out, err)
	}
	if errOut != "sock: the machine key stays: sessions here can still message you; sock settings --message-owner=off stops that\n" {
		t.Fatalf("stderr %q", errOut)
	}
	if fc, _ := client.LoadFileConfig(app.ConfigPath); fc.MachineKey != socktest.MachineKeyToken || fc.Token != "" {
		t.Fatalf("config %+v", fc)
	}
	// owner messaging already off: the key stays, but nothing messages
	writeConfig(t, app, fmt.Sprintf("url = %q\ntoken = %q\nmachine_key = %q\nmessage_owner = false\n", srv.URL, socktest.HumanToken, socktest.MachineKeyToken))
	if _, errOut, err := run(t, app, nil, "logout"); err != nil || errOut != "sock: the machine key stays; owner messaging is off\n" {
		t.Fatalf("logout with message_owner off: %q, %v", errOut, err)
	}
	// no machine key: nothing stays, so nothing is said
	writeConfig(t, app, fmt.Sprintf("url = %q\ntoken = %q\n", srv.URL, socktest.HumanToken))
	if _, errOut, err := run(t, app, nil, "logout"); err != nil || errOut != "" {
		t.Fatalf("logout without a key: %v, %q", err, errOut)
	}
}

func TestLogoutAlreadyRevokedRemovesToken(t *testing.T) {
	srv := socktest.New(t)
	srv.Fail = map[string]error{"SignOut": socktest.Error(connect.CodeUnauthenticated, sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID)}
	app := humanApp(t, srv, nil)
	writeConfig(t, app, fmt.Sprintf("url = %q\ntoken = %q\n", srv.URL, socktest.HumanToken))
	if out, _, err := run(t, app, nil, "logout"); err != nil || out != "signed out\n" {
		t.Fatalf("logout: %q, %v", out, err)
	}
	if fileToken(t, app) != "" {
		t.Fatal("token kept")
	}
}

func TestLogoutUnavailableKeepsToken(t *testing.T) {
	srv := socktest.New(t)
	srv.Fail = map[string]error{"SignOut": socktest.Unavailable()}
	app := humanApp(t, srv, nil)
	writeConfig(t, app, fmt.Sprintf("token = %q\n", socktest.HumanToken))
	if _, _, err := run(t, app, nil, "logout"); err == nil {
		t.Fatal("logout succeeded while the server was unavailable")
	}
	if fileToken(t, app) != socktest.HumanToken {
		t.Fatal("token removed")
	}
}

func TestLogoutRefusesPuppetToken(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	writeConfig(t, app, fmt.Sprintf("token = %q\n", socktest.Token))
	_, _, err := run(t, app, nil, "logout")
	if err == nil || !strings.Contains(err.Error(), "puppet") {
		t.Fatalf("got %v", err)
	}
	if len(srv.CallsTo("SignOut")) != 0 || fileToken(t, app) != socktest.Token {
		t.Fatal("puppet token signed out")
	}
}

func TestLogoutWithoutToken(t *testing.T) {
	srv := socktest.New(t)
	if _, _, err := run(t, humanApp(t, srv, nil), nil, "logout"); err == nil {
		t.Fatal("logout with no token succeeded")
	}
}

func TestLoginKeepsTokenWrittenDuringTheWait(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	b := withBrowser(t, app, srv)
	app.OpenBrowser = func(u string) error {
		writeConfig(t, app, "token = \"sock_pt_written_meanwhile\"\n")
		return b.open(u)
	}
	_, _, err := run(t, app, nil, "login")
	if err == nil || !strings.Contains(err.Error(), "already holds a token") {
		t.Fatalf("got %v", err)
	}
	if fileToken(t, app) != "sock_pt_written_meanwhile" {
		t.Fatal("token written during the wait was overwritten")
	}
}

func TestLoginSaysWhenTheCallbackArrives(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	_, errOut, err := run(t, app, nil, "login")
	if err != nil {
		t.Fatalf("login: %v, stderr %q", err, errOut)
	}
	if !strings.Contains(errOut, "sign-in received") {
		t.Fatalf("stderr %q does not say the callback arrived", errOut)
	}
}

// Sign in once and puppets register themselves: login also mints a machine
// key for this host and keeps it, so sock-mcp makes each project its own
// puppet with no further step.
func TestLoginAlsoKeepsAMachineKey(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	out, errOut, err := run(t, app, nil, "login")
	if err != nil {
		t.Fatalf("login: %v, stderr %q", err, errOut)
	}
	if out != "signed in as @guy on device "+socktest.HumanDeviceID+"\n" {
		t.Fatalf("stdout %q", out)
	}
	if strings.Contains(out+errOut, "sock_mk_") || strings.Contains(out+errOut, socktest.HumanToken) {
		t.Fatalf("a secret was printed: %q %q", out, errOut)
	}
	fc, err := client.LoadFileConfig(app.ConfigPath)
	if err != nil || fc.MachineKey != "sock_mk_device-mk_secret" || fc.Token != socktest.HumanToken {
		t.Fatalf("config %+v, %v", fc, err)
	}
	calls := srv.CallsTo("MintMachineKey")
	if len(calls) != 1 {
		t.Fatalf("%d MintMachineKey calls", len(calls))
	}
	if got := calls[0].Header.Get("Authorization"); got != "Bearer "+socktest.HumanToken {
		t.Fatalf("minted with %q, want the new login", got)
	}
	if label := calls[0].Msg.(*sockv1.MintMachineKeyRequest).GetLabel(); label != "box" {
		t.Fatalf("label %q, want the short hostname", label)
	}
	if !strings.Contains(errOut, "machine key") {
		t.Fatalf("stderr %q does not say a machine key was kept", errOut)
	}
}

// A machine key the file already holds is never replaced by a login while
// the server accepts it.
func TestLoginKeepsAnExistingMachineKey(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	writeConfig(t, app, fmt.Sprintf("url = %q\nmachine_key = %q\n", srv.URL, socktest.MachineKeyToken))
	_, errOut, err := run(t, app, nil, "login")
	if err != nil {
		t.Fatalf("login: %v, stderr %q", err, errOut)
	}
	if fc, _ := client.LoadFileConfig(app.ConfigPath); fc.MachineKey != socktest.MachineKeyToken {
		t.Fatalf("machine_key replaced: %+v", fc)
	}
	if n := len(srv.CallsTo("MintMachineKey")); n != 0 {
		t.Fatalf("%d machine keys minted though the file had one", n)
	}
	if !strings.Contains(errOut, "sock: machine key works\n") {
		t.Fatalf("stderr %q", errOut)
	}
}

// fileSignedIn writes a file holding a session and a machine key for srv, as a
// login writes it, with GetMe answering the human.
func fileSignedIn(t *testing.T, srv *socktest.Server, app *App, token, key string) string {
	t.Helper()
	srv.Me = socktest.Human()
	body := fmt.Sprintf("url = %q\ntoken = %q\n", srv.URL, token)
	if key != "" {
		body += fmt.Sprintf("machine_key = %q\n", key)
	}
	writeConfig(t, app, body)
	return body
}

func fileBody(t *testing.T, app *App) string {
	t.Helper()
	b, err := os.ReadFile(app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// hubWrites are the calls that change something at the hub.
func hubWrites(srv *socktest.Server) int {
	n := 0
	for _, m := range []string{"StartTerminalSignIn", "FinishTerminalSignIn", "RegisterWithEmail", "MintMachineKey", "SignOut"} {
		n += len(srv.CallsTo(m))
	}
	return n
}

// A valid session is never replaced: no browser, no sign-in, and a valid
// machine key beside it is only checked.
func TestLoginAlreadySignedIn(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil) // its OpenBrowser fails the test
	body := fileSignedIn(t, srv, app, socktest.HumanToken, socktest.MachineKeyToken)
	out, errOut, err := run(t, app, nil, "login")
	if err != nil {
		t.Fatalf("login: %v, stderr %q", err, errOut)
	}
	if out != "already signed in as @guy\n" || errOut != "sock: machine key works\n" {
		t.Fatalf("stdout %q, stderr %q", out, errOut)
	}
	if n := hubWrites(srv); n != 0 {
		t.Fatalf("%d hub writes", n)
	}
	if fileBody(t, app) != body {
		t.Fatal("config file changed")
	}
	if got := srv.CallsTo("GetMe")[0].Header.Get("Authorization"); got != "Bearer "+socktest.HumanToken {
		t.Fatalf("session checked with %q", got)
	}
	if got := srv.CallsTo("ListPuppets")[0].Header.Get("Authorization"); got != "Bearer "+socktest.MachineKeyToken {
		t.Fatalf("machine key checked with %q", got)
	}
}

// A second login after a first one makes no hub write.
func TestLoginIsIdempotent(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	b := withBrowser(t, app, srv)
	if _, errOut, err := run(t, app, nil, "login"); err != nil {
		t.Fatalf("first login: %v, %q", err, errOut)
	}
	srv.Me = socktest.Human()
	writes, body := hubWrites(srv), fileBody(t, app)
	out, errOut, err := run(t, app, nil, "login")
	if err != nil || out != "already signed in as @guy\n" || errOut != "sock: machine key works\n" {
		t.Fatalf("second login: %v, %q, %q", err, out, errOut)
	}
	if hubWrites(srv) != writes || len(b.opened) != 1 || fileBody(t, app) != body {
		t.Fatal("second login wrote")
	}
}

// A session the hub rejects is removed before sign-in starts, then replaced.
func TestLoginReplacesARejectedSession(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	b := withBrowser(t, app, srv)
	fileSignedIn(t, srv, app, "sock_st_dead_secret", socktest.MachineKeyToken)
	app.OpenBrowser = func(u string) error {
		if tok := fileToken(t, app); tok != "" {
			t.Errorf("rejected session %q still in the file during sign-in", tok)
		}
		return b.open(u)
	}
	out, errOut, err := run(t, app, nil, "login")
	if err != nil {
		t.Fatalf("login: %v, stderr %q", err, errOut)
	}
	if out != "signed in as @guy on device "+socktest.HumanDeviceID+"\n" || fileToken(t, app) != socktest.HumanToken {
		t.Fatalf("stdout %q, token %q", out, fileToken(t, app))
	}
	if strings.Contains(out+errOut, "sock_st_dead_secret") {
		t.Fatal("the rejected token was printed")
	}
	if !strings.Contains(errOut, "sock: machine key works\n") || len(srv.CallsTo("MintMachineKey")) != 0 {
		t.Fatalf("stderr %q", errOut)
	}
}

// A session that cannot be checked is left alone: exit 1, file untouched.
func TestLoginUncheckedSessionChangesNothing(t *testing.T) {
	srv := socktest.New(t)
	srv.Fail = map[string]error{"GetMe": socktest.Unavailable()}
	app := humanApp(t, srv, nil)
	body := fileSignedIn(t, srv, app, socktest.HumanToken, "")
	_, _, err := run(t, app, nil, "login")
	if err == nil || !strings.Contains(err.Error(), "could not check the session") {
		t.Fatalf("got %v", err)
	}
	if fileBody(t, app) != body || hubWrites(srv) != 0 {
		t.Fatal("unchecked session touched")
	}
}

// With a valid session and no machine key, login mints one.
func TestLoginMintsAMissingMachineKey(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	fileSignedIn(t, srv, app, socktest.HumanToken, "")
	out, errOut, err := run(t, app, nil, "login")
	if err != nil || out != "already signed in as @guy\n" {
		t.Fatalf("login: %v, %q", err, out)
	}
	if errOut != "sock: kept a machine key for box: each project you open gets its own puppet\n" {
		t.Fatalf("stderr %q", errOut)
	}
	if fc, _ := client.LoadFileConfig(app.ConfigPath); fc.MachineKey != "sock_mk_device-mk_secret" {
		t.Fatalf("config %+v", fc)
	}
}

// A machine key the hub rejects is replaced without revoking it.
func TestLoginReplacesARejectedMachineKey(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	fileSignedIn(t, srv, app, socktest.HumanToken, "sock_mk_dead_secret")
	out, errOut, err := run(t, app, nil, "login")
	if err != nil || out != "already signed in as @guy\n" {
		t.Fatalf("login: %v, %q", err, out)
	}
	if errOut != "sock: replaced a machine key the server no longer accepts\n" {
		t.Fatalf("stderr %q", errOut)
	}
	if fc, _ := client.LoadFileConfig(app.ConfigPath); fc.MachineKey != "sock_mk_device-mk_secret" || fc.Token != socktest.HumanToken {
		t.Fatalf("config %+v", fc)
	}
	if n := len(srv.CallsTo("RevokeDevice")); n != 0 {
		t.Fatalf("%d revokes", n)
	}
}

// A machine key that cannot be checked is kept, with a warning.
func TestLoginKeepsAnUncheckedMachineKey(t *testing.T) {
	srv := socktest.New(t)
	srv.Fail = map[string]error{"ListPuppets": socktest.Unavailable()}
	app := humanApp(t, srv, nil)
	body := fileSignedIn(t, srv, app, socktest.HumanToken, socktest.MachineKeyToken)
	out, errOut, err := run(t, app, nil, "login")
	if err != nil || out != "already signed in as @guy\n" {
		t.Fatalf("login: %v, %q", err, out)
	}
	if !strings.HasPrefix(errOut, "sock: could not check the machine key (") || !strings.HasSuffix(errOut, "); kept it\n") {
		t.Fatalf("stderr %q", errOut)
	}
	if fileBody(t, app) != body || hubWrites(srv) != 0 {
		t.Fatal("unchecked machine key touched")
	}
}

// The file's credentials belong to its url, the default when it names none:
// a login at another server never sends them there.
func TestLoginRefusesAnotherServerForTheFilesCredentials(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil) // SOCK_URL is srv
	for _, body := range []string{
		fmt.Sprintf("machine_key = %q\n", socktest.MachineKeyToken),
		fmt.Sprintf("token = %q\n", socktest.HumanToken),
	} {
		writeConfig(t, app, body)
		if _, _, err := run(t, app, nil, "login"); err == nil {
			t.Fatalf("%q: login at another server ran", body)
		}
		if len(srv.Calls) != 0 || fileBody(t, app) != body {
			t.Fatalf("%q: credentials sent to SOCK_URL", body)
		}
	}
}

// Signing in worked; a key that could not be minted must not undo it.
func TestLoginSurvivesAMachineKeyFailure(t *testing.T) {
	srv := socktest.New(t)
	srv.MintMachineKeyErr = connect.NewError(connect.CodeUnavailable, errors.New("down"))
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	out, errOut, err := run(t, app, nil, "login")
	if err != nil {
		t.Fatalf("login failed because the key could not be minted: %v", err)
	}
	if !strings.Contains(out, "signed in as @guy") || fileToken(t, app) != socktest.HumanToken {
		t.Fatalf("login not completed: %q", out)
	}
	if !strings.Contains(errOut, "sock machine-key create") {
		t.Fatalf("stderr %q does not say how to finish", errOut)
	}
}

func TestLoginWritesSessionToken(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	b := withBrowser(t, app, srv)
	out, errOut, err := run(t, app, nil, "login")
	if err != nil {
		t.Fatalf("login: %v, stderr %q", err, errOut)
	}
	if out != "signed in as @guy on device "+socktest.HumanDeviceID+"\n" {
		t.Fatalf("stdout %q", out)
	}
	if strings.Contains(out+errOut, socktest.HumanToken) || strings.Contains(out+errOut, socktest.TerminalToken) {
		t.Fatal("a token was printed")
	}
	for _, want := range []string{srv.URL + "/login", socktest.UserCode, "Opened your browser", "sign-in received"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr %q lacks %q", errOut, want)
		}
	}
	if len(b.opened) != 1 {
		t.Fatalf("browser opened %d times", len(b.opened))
	}
	if fileToken(t, app) != socktest.HumanToken {
		t.Fatal("token not written")
	}
	fi, err := os.Stat(app.ConfigPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v, %v", fi.Mode(), err)
	}
	starts := srv.CallsTo("StartTerminalSignIn")
	if len(starts) != 1 || starts[0].Msg.(*sockv1.StartTerminalSignInRequest).GetDeviceLabel() != "sock CLI on box" || starts[0].Header.Get("Authorization") != "" {
		t.Fatalf("start calls %v", starts)
	}
	finishes := srv.CallsTo("FinishTerminalSignIn")
	if len(finishes) != 1 || finishes[0].Msg.(*sockv1.FinishTerminalSignInRequest).GetTerminalSignIn() != socktest.TerminalToken || finishes[0].Header.Get("Authorization") != "" {
		t.Fatalf("finish calls %v", finishes)
	}
	if len(srv.CallsTo("RegisterWithEmail")) != 0 {
		t.Fatal("registered a person who has an account")
	}
}

func TestLoginKeepsURLAndSendsGate(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	app.Getenv = func(k string) string { return map[string]string{"SOCK_ACCESS_GATE": "gate-1"}[k] }
	writeConfig(t, app, fmt.Sprintf("url = %q\n", srv.URL))
	withBrowser(t, app, srv)
	if _, errOut, err := run(t, app, nil, "login"); err != nil {
		t.Fatalf("login: %v %s", err, errOut)
	}
	cfg, err := config.Load(config.Sources{Getenv: func(string) string { return "" }, Path: app.ConfigPath})
	if err != nil || cfg.URL != srv.URL || cfg.Token != socktest.HumanToken {
		t.Fatalf("config %+v, %v", cfg, err)
	}
	for _, m := range []string{"StartTerminalSignIn", "FinishTerminalSignIn"} {
		if g := srv.CallsTo(m)[0].Header.Get("Sock-Access-Gate"); g != "gate-1" {
			t.Fatalf("%s gate %q", m, g)
		}
	}
}

// The browser may be on another device: the page and the code are printed
// whether or not one opens here.
func TestLoginAlwaysPrintsThePageAndTheCode(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil) // its OpenBrowser fails the test
	_, errOut, err := run(t, app, nil, "login", "--no-browser")
	if err != nil || !strings.Contains(errOut, srv.URL+"/login") || !strings.Contains(errOut, socktest.UserCode) || !strings.Contains(errOut, "Open it on any device") {
		t.Fatalf("no-browser: %v %q", err, errOut)
	}

	app = humanApp(t, srv, nil)
	app.OpenBrowser = func(string) error { return errors.New("no display") }
	_, errOut, err = run(t, app, nil, "login")
	if err != nil || !strings.Contains(errOut, "could not open a browser: no display") || !strings.Contains(errOut, socktest.UserCode) || !strings.Contains(errOut, "Open it on any device") {
		t.Fatalf("browser failure: %v %q", err, errOut)
	}
}

// A page the hub names off its own host is printed, never handed to the
// opener: the opener runs whatever scheme it is given.
func TestLoginOpensOnlyTheHubsOwnPage(t *testing.T) {
	var opened []string
	open := func(u string) error { opened = append(opened, u); return nil }
	for _, bad := range []string{"https://evil.example/login?code=X", "file:///etc/passwd", "http://127.0.0.1:1/login", "javascript:alert(1)"} {
		if err := openOnHub(open, "https://hub.test", bad); err == nil {
			t.Errorf("%q opened", bad)
		}
	}
	if err := openOnHub(open, "https://hub.test", "https://hub.test/login?code=X"); err != nil || len(opened) != 1 {
		t.Fatalf("own page: %v %v", err, opened)
	}
}

func pending(d time.Duration) *sockv1.FinishTerminalSignInResponse {
	return &sockv1.FinishTerminalSignInResponse{Result: &sockv1.FinishTerminalSignInResponse_Pending{Pending: &sockv1.TerminalSignInPending{RetryAfter: durationpb.New(d)}}}
}

const ticket = "sock_rg_ticket"

func registration() *sockv1.FinishTerminalSignInResponse {
	return &sockv1.FinishTerminalSignInResponse{Result: &sockv1.FinishTerminalSignInResponse_Registration{Registration: &sockv1.RegistrationTicket{RegistrationTicket: ticket}}}
}

// login waits a full interval before its first poll (Start counted as one)
// and then as long as each pending answer says.
func TestLoginWaitsWhilePending(t *testing.T) {
	srv := socktest.New(t)
	srv.Finishes = []*sockv1.FinishTerminalSignInResponse{pending(2 * time.Second), pending(3 * time.Second)}
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	var slept []time.Duration
	app.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	if _, errOut, err := run(t, app, nil, "login"); err != nil {
		t.Fatalf("login: %v %q", err, errOut)
	}
	if len(srv.CallsTo("FinishTerminalSignIn")) != 3 {
		t.Fatalf("%d polls", len(srv.CallsTo("FinishTerminalSignIn")))
	}
	if want := []time.Duration{5 * time.Second, 2 * time.Second, 3 * time.Second}; fmt.Sprint(slept) != fmt.Sprint(want) {
		t.Fatalf("slept %v, want %v", slept, want)
	}
}

func TestLoginGivesUpAtItsTimeout(t *testing.T) {
	srv := socktest.New(t)
	for range 10000 {
		srv.Finishes = append(srv.Finishes, pending(time.Millisecond))
	}
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	app.LoginTimeout = 50 * time.Millisecond
	app.Sleep = func(ctx context.Context, d time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
			return nil
		}
	}
	_, _, err := run(t, app, nil, "login")
	if err == nil || !strings.Contains(err.Error(), "sign-in timed out; run sock login again") {
		t.Fatalf("got %v", err)
	}
	if fileToken(t, app) != "" {
		t.Fatal("token written after a timeout")
	}
}

func TestLoginRefusals(t *testing.T) {
	for reason, want := range map[sockv1.ErrorReason]string{
		sockv1.ErrorReason_ERROR_REASON_NOT_REGISTERED:           "no sock account for that sign-in",
		sockv1.ErrorReason_ERROR_REASON_EMAIL_CODE_INVALID:       "no sock account for that sign-in",
		sockv1.ErrorReason_ERROR_REASON_TERMINAL_SIGN_IN_INVALID: "run sock login again",
	} {
		srv := socktest.New(t)
		srv.Fail = map[string]error{"FinishTerminalSignIn": socktest.Error(connect.CodeUnauthenticated, reason)}
		app := humanApp(t, srv, nil)
		withBrowser(t, app, srv)
		_, _, err := run(t, app, nil, "login")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", reason, err)
		}
		if fileToken(t, app) != "" {
			t.Errorf("%s: token written after a refusal", reason)
		}
	}
}

func TestLoginLabel(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	app.Hostname = func() (string, error) { return strings.Repeat("é", 80), nil }
	withBrowser(t, app, srv)
	if _, _, err := run(t, app, nil, "login"); err != nil {
		t.Fatal(err)
	}
	label := srv.CallsTo("StartTerminalSignIn")[0].Msg.(*sockv1.StartTerminalSignInRequest).GetDeviceLabel()
	if utf8.RuneCountInString(label) != 64 || !strings.HasPrefix(label, "sock CLI on é") {
		t.Fatalf("default label %q", label)
	}

	app = humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	if _, _, err := run(t, app, nil, "login", "--label", "work laptop"); err != nil {
		t.Fatal(err)
	}
	if got := srv.CallsTo("StartTerminalSignIn")[1].Msg.(*sockv1.StartTerminalSignInRequest).GetDeviceLabel(); got != "work laptop" {
		t.Fatalf("label %q", got)
	}
	app = humanApp(t, srv, nil)
	if _, _, err := run(t, app, nil, "login", "--label", strings.Repeat("x", 65)); err == nil {
		t.Fatal("65-character label accepted")
	}
}

// A sign-in to an address with no account: the ticket registers one, with
// the flags off a terminal.
func TestLoginRegistersWithFlags(t *testing.T) {
	srv := socktest.New(t)
	srv.Finishes = []*sockv1.FinishTerminalSignInResponse{registration()}
	srv.InviteRequired = true
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	out, errOut, err := run(t, app, nil, "login", "--invite", "inv-1", "--handle", "@guy", "--name", "Guy G", "--accept-terms", "2026-10-11")
	if err != nil {
		t.Fatalf("login: %v %s", err, errOut)
	}
	if out != "signed in as @guy on device "+socktest.HumanDeviceID+"\n" || fileToken(t, app) != socktest.HumanToken {
		t.Fatalf("stdout %q", out)
	}
	if !strings.Contains(errOut, "No sock account for this address yet") {
		t.Fatalf("stderr %q", errOut)
	}
	req := srv.CallsTo("RegisterWithEmail")[0].Msg.(*sockv1.RegisterWithEmailRequest)
	if req.GetRegistrationTicket() != ticket || req.GetInviteCode() != "inv-1" || req.GetHandle() != "guy" || req.GetDisplayName() != "Guy G" ||
		req.GetAcceptedTermsVersion() != "2026-10-11" || req.GetDeviceLabel() != "sock CLI on box" || req.GetCode() != "" {
		t.Fatalf("request %v", req)
	}
	if len(srv.CallsTo("GetRegistrationPolicy")) != 1 {
		t.Fatal("policy not read")
	}
}

func TestLoginRegistersFromPrompts(t *testing.T) {
	for _, tc := range []struct {
		invite            bool
		stdin, wantInvite string
	}{
		{false, "guy\nGuy G\nyes\n", ""},
		{true, "@guy\nGuy G\ninv-1\nyes\n", "inv-1"},
	} {
		srv := socktest.New(t)
		srv.Finishes = []*sockv1.FinishTerminalSignInResponse{registration()}
		srv.InviteRequired = tc.invite
		app := humanApp(t, srv, nil)
		app.StdinIsTTY = func() bool { return true }
		withBrowser(t, app, srv)
		_, errOut, err := run(t, app, strings.NewReader(tc.stdin), "login")
		if err != nil {
			t.Fatalf("invite %v: %v %q", tc.invite, err, errOut)
		}
		for _, want := range []string{"Handle", "Display name", termsURL, privacyURL, termsVersion} {
			if !strings.Contains(errOut, want) {
				t.Errorf("stderr %q lacks %q", errOut, want)
			}
		}
		if strings.Contains(errOut, inviteURL) != tc.invite {
			t.Errorf("invite %v: stderr %q", tc.invite, errOut)
		}
		req := srv.CallsTo("RegisterWithEmail")[0].Msg.(*sockv1.RegisterWithEmailRequest)
		if req.GetHandle() != "guy" || req.GetDisplayName() != "Guy G" || req.GetInviteCode() != tc.wantInvite || req.GetAcceptedTermsVersion() != termsVersion {
			t.Fatalf("request %v", req)
		}
	}
}

func TestLoginRegistrationNeedsFlagsOffATerminal(t *testing.T) {
	srv := socktest.New(t)
	srv.Finishes = []*sockv1.FinishTerminalSignInResponse{registration()}
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	_, _, err := run(t, app, nil, "login", "--name", "Guy")
	if err == nil || !strings.Contains(err.Error(), "--handle, --name and --accept-terms") {
		t.Fatalf("got %v", err)
	}
	if len(srv.CallsTo("RegisterWithEmail")) != 0 || fileToken(t, app) != "" {
		t.Fatal("registered without the flags")
	}
	// a bad handle flag is refused before anything is sent
	srv.Finishes = []*sockv1.FinishTerminalSignInResponse{registration()}
	_, _, err = run(t, humanApp(t, srv, nil), nil, "login", "--no-browser", "--handle", "Guy!", "--name", "Guy", "--accept-terms", termsVersion)
	if err == nil || len(srv.CallsTo("RegisterWithEmail")) != 0 {
		t.Fatalf("bad handle: %v", err)
	}
}

func TestLoginRegistrationDeclinedTerms(t *testing.T) {
	srv := socktest.New(t)
	srv.Finishes = []*sockv1.FinishTerminalSignInResponse{registration()}
	app := humanApp(t, srv, nil)
	app.StdinIsTTY = func() bool { return true }
	withBrowser(t, app, srv)
	_, _, err := run(t, app, strings.NewReader("guy\nGuy\nno\n"), "login")
	if err == nil || err.Error() != "not registered" || len(srv.CallsTo("RegisterWithEmail")) != 0 {
		t.Fatalf("got %v", err)
	}
}

func TestLoginRegistrationStaleTerms(t *testing.T) {
	srv := socktest.New(t)
	srv.Finishes = []*sockv1.FinishTerminalSignInResponse{registration()}
	srv.Fail = map[string]error{"RegisterWithEmail": socktest.Error(connect.CodeFailedPrecondition, sockv1.ErrorReason_ERROR_REASON_TERMS_NOT_ACCEPTED)}
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	_, _, err := run(t, app, nil, "login", "--handle", "guy", "--name", "Guy", "--accept-terms", "2020-01-01")
	if err == nil || !strings.Contains(err.Error(), "update sock") {
		t.Fatalf("got %v", err)
	}
}

func TestLoginHelpNamesTerms(t *testing.T) {
	out, _, err := run(t, DefaultApp("test"), nil, "login", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://joinsock.com/terms/", "https://joinsock.com/privacy/", "2026-10-11", "--no-browser"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--port") {
		t.Fatal("help still offers --port")
	}
}

func TestRegisterIsGone(t *testing.T) {
	_, _, err := run(t, DefaultApp("test"), nil, "register", "--handle", "guy")
	if err == nil || !strings.Contains(err.Error(), "sock register is gone") {
		t.Fatalf("got %v", err)
	}
	out, _, _ := run(t, DefaultApp("test"), nil, "--help")
	if strings.Contains(out, "register") {
		t.Fatal("register still listed in help")
	}
}

// A 401 with no TOKEN_INVALID reason (a proxy in front of the hub) is not the
// hub rejecting the credential: nothing is removed or replaced.
func TestLoginTreatsABare401AsUnchecked(t *testing.T) {
	bare := connect.NewError(connect.CodeUnauthenticated, errors.New("proxy says no"))
	srv := socktest.New(t)
	srv.Fail = map[string]error{"GetMe": bare}
	app := humanApp(t, srv, nil)
	body := fileSignedIn(t, srv, app, socktest.HumanToken, socktest.MachineKeyToken)
	if _, _, err := run(t, app, nil, "login"); err == nil || !strings.Contains(err.Error(), "could not check the session") {
		t.Fatalf("session: %v", err)
	}
	if fileBody(t, app) != body || hubWrites(srv) != 0 {
		t.Fatal("session removed on a bare 401")
	}

	srv.Fail = map[string]error{"ListPuppets": bare}
	_, errOut, err := run(t, app, nil, "login")
	if err != nil || !strings.HasPrefix(errOut, "sock: could not check the machine key (") {
		t.Fatalf("key: %v, %q", err, errOut)
	}
	if fileBody(t, app) != body || hubWrites(srv) != 0 {
		t.Fatal("machine key replaced on a bare 401")
	}
}

// A machine key written while login replaced a rejected one is kept, and the
// key login minted is named for revoking.
func TestLoginKeepsAMachineKeyWrittenDuringTheReplace(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	fileSignedIn(t, srv, app, socktest.HumanToken, "sock_mk_dead_secret")
	newer := fmt.Sprintf("url = %q\ntoken = %q\nmachine_key = %q\n", srv.URL, socktest.HumanToken, socktest.MachineKeyToken)
	// the second hostname lookup names the key to mint, after the check
	lookups := 0
	app.Hostname = func() (string, error) {
		if lookups++; lookups == 2 {
			writeConfig(t, app, newer)
		}
		return "box", nil
	}
	_, errOut, err := run(t, app, nil, "login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if errOut != "sock: a newer machine key is in the config file, so it was kept; revoke the one just minted with sock devices revoke device-mk\n" {
		t.Fatalf("stderr %q", errOut)
	}
	if fileBody(t, app) != newer {
		t.Fatal("the newer machine key was overwritten")
	}
}
