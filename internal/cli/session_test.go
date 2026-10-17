package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"

	"github.com/guygrigsby/sock-cli/internal/config"
	"github.com/guygrigsby/sock-cli/internal/socktest"
	client "github.com/guygrigsby/sock-client"
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

// browser plays the person's browser, the hub's interstitial and Apple: it
// takes the login URL and delivers the relayed callback to the listener.
type browser struct {
	t        *testing.T
	hub      string
	opened   int
	nonceSum string
}

func (b *browser) open(raw string) error {
	b.opened++
	u, err := url.Parse(raw)
	if err != nil || u.Scheme+"://"+u.Host+u.Path != b.hub+"/apple/cli-login" {
		b.t.Errorf("login URL %q is not the hub's /apple/cli-login", raw)
		return errors.New("bad URL")
	}
	q := u.Query()
	b.nonceSum = q.Get("nonce")
	state := q.Get("state")
	port, _, _ := strings.Cut(state, ".")
	relay := fmt.Sprintf("http://127.0.0.1:%s/callback?%s", port, url.Values{"state": {state}, "code": {"code-1"}, "id_token": {"h.p.s"}}.Encode())
	res, err := http.Get(relay) // follows the 303 to /done
	if err != nil {
		b.t.Errorf("relay: %v", err)
		return err
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if res.Request.URL.Path != "/done" || res.StatusCode != http.StatusOK {
		b.t.Errorf("browser ended at %s with %d", res.Request.URL, res.StatusCode)
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
	if strings.Contains(out+errOut, socktest.HumanToken) {
		t.Fatal("session token printed")
	}
	if fileToken(t, app) != socktest.HumanToken {
		t.Fatal("token not written")
	}
	fi, err := os.Stat(app.ConfigPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v, %v", fi.Mode(), err)
	}
	calls := srv.CallsTo("SignInWithApple")
	if len(calls) != 1 {
		t.Fatalf("%d SignInWithApple calls", len(calls))
	}
	req := calls[0].Msg.(*sockv1.SignInWithAppleRequest)
	sum := sha256.Sum256([]byte(req.GetNonce()))
	if req.GetAppleClient() != sockv1.AppleClient_APPLE_CLIENT_WEB || req.GetAuthorizationCode() != "code-1" ||
		req.GetIdentityToken() != "h.p.s" || req.GetDeviceLabel() != "sock CLI on box" || hex.EncodeToString(sum[:]) != b.nonceSum {
		t.Fatalf("request %v (nonce hash in URL %s)", req, b.nonceSum)
	}
	if h := calls[0].Header.Get("Authorization"); h != "" {
		t.Fatalf("anonymous call sent Authorization %q", h)
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
	if g := srv.CallsTo("SignInWithApple")[0].Header.Get("Sock-Access-Gate"); g != "gate-1" {
		t.Fatalf("gate %q", g)
	}
}

func TestLoginRefusesExistingToken(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	writeConfig(t, app, "token = \"sock_pt_keepme_secret\"\n")
	_, _, err := run(t, app, nil, "login")
	if err == nil || !strings.Contains(err.Error(), "already holds a token") {
		t.Fatalf("got %v", err)
	}
	if fileToken(t, app) != "sock_pt_keepme_secret" || len(srv.CallsTo("SignInWithApple")) != 0 {
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
	if h := srv.CallsTo("SignInWithApple")[0].Header.Get("Authorization"); h != "" {
		t.Fatalf("SOCK_TOKEN sent on sign-in: %q", h)
	}
}

func TestLoginNoBrowserPrintsURL(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	b := &browser{t: t, hub: srv.URL}
	errOut := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		root := NewRoot(app)
		root.SetArgs([]string{"login", "--no-browser"})
		root.SetIn(strings.NewReader(""))
		root.SetOut(io.Discard)
		root.SetErr(errOut)
		done <- root.Execute()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(errOut.String(), "/apple/cli-login?") {
		if time.Now().After(deadline) {
			t.Fatalf("no URL printed: %q", errOut.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	var raw string
	for _, f := range strings.Fields(errOut.String()) {
		if strings.Contains(f, "/apple/cli-login?") {
			raw = f
		}
	}
	if err := b.open(raw); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("login: %v", err)
	}
	if fileToken(t, app) != socktest.HumanToken {
		t.Fatal("token not written")
	}
}

func TestLoginBrowserFailurePrintsURL(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	app.LoginTimeout = 100 * time.Millisecond
	app.OpenBrowser = func(string) error { return errors.New("no display") }
	_, errOut, err := run(t, app, nil, "login")
	if err == nil || !strings.Contains(errOut, "/apple/cli-login?") {
		t.Fatalf("err %v, stderr %q", err, errOut)
	}
}

func TestLoginNotRegistered(t *testing.T) {
	srv := socktest.New(t)
	srv.Fail = map[string]error{"SignInWithApple": socktest.Error(connect.CodeNotFound, sockv1.ErrorReason_ERROR_REASON_NOT_REGISTERED)}
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	_, _, err := run(t, app, nil, "login")
	if err == nil || !strings.Contains(err.Error(), "run sock register with an invite") {
		t.Fatalf("got %v", err)
	}
	if fileToken(t, app) != "" {
		t.Fatal("token written after a refusal")
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

func TestLoginLabel(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	app.Hostname = func() (string, error) { return strings.Repeat("é", 80), nil }
	withBrowser(t, app, srv)
	if _, _, err := run(t, app, nil, "login"); err != nil {
		t.Fatal(err)
	}
	label := srv.CallsTo("SignInWithApple")[0].Msg.(*sockv1.SignInWithAppleRequest).GetDeviceLabel()
	if utf8.RuneCountInString(label) != 64 || !strings.HasPrefix(label, "sock CLI on é") {
		t.Fatalf("default label %q", label)
	}

	app = humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	if _, _, err := run(t, app, nil, "login", "--label", "work laptop"); err != nil {
		t.Fatal(err)
	}
	if got := srv.CallsTo("SignInWithApple")[1].Msg.(*sockv1.SignInWithAppleRequest).GetDeviceLabel(); got != "work laptop" {
		t.Fatalf("label %q", got)
	}
	app = humanApp(t, srv, nil)
	if _, _, err := run(t, app, nil, "login", "--label", strings.Repeat("x", 65)); err == nil {
		t.Fatal("65-character label accepted")
	}
}

func TestLoginPortChecked(t *testing.T) {
	srv := socktest.New(t)
	_, _, err := run(t, humanApp(t, srv, nil), nil, "login", "--port", "80")
	if err == nil || !strings.Contains(err.Error(), "1024 to 65535") {
		t.Fatalf("got %v", err)
	}
}

func TestRegister(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	out, errOut, err := run(t, app, nil, "register", "--invite", "inv-1", "--handle", "@guy", "--name", "Guy G", "--accept-terms", "2026-10-11")
	if err != nil {
		t.Fatalf("register: %v %s", err, errOut)
	}
	if out != "signed in as @guy on device "+socktest.HumanDeviceID+"\n" || fileToken(t, app) != socktest.HumanToken {
		t.Fatalf("stdout %q", out)
	}
	req := srv.CallsTo("RegisterWithApple")[0].Msg.(*sockv1.RegisterWithAppleRequest)
	if req.GetInviteCode() != "inv-1" || req.GetHandle() != "guy" || req.GetDisplayName() != "Guy G" || req.GetAcceptedTermsVersion() != "2026-10-11" ||
		req.GetAppleClient() != sockv1.AppleClient_APPLE_CLIENT_WEB || req.GetAuthorizationCode() != "code-1" || req.GetNonce() == "" {
		t.Fatalf("request %v", req)
	}
}

func TestRegisterWithoutInvite(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	if _, _, err := run(t, app, nil, "register", "--handle", "guy", "--name", "Guy", "--accept-terms", "2026-10-11"); err != nil {
		t.Fatal(err)
	}
	if inv := srv.CallsTo("RegisterWithApple")[0].Msg.(*sockv1.RegisterWithAppleRequest).GetInviteCode(); inv != "" {
		t.Fatalf("invite %q", inv)
	}
}

func TestRegisterNeedsTermsHandleAndName(t *testing.T) {
	srv := socktest.New(t)
	for _, args := range [][]string{
		{"register", "--handle", "guy", "--name", "Guy"},
		{"register", "--name", "Guy", "--accept-terms", "v"},
		{"register", "--handle", "guy", "--accept-terms", "v"},
	} {
		if _, _, err := run(t, humanApp(t, srv, nil), nil, args...); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}

func TestRegisterHelpNamesTerms(t *testing.T) {
	out, _, err := run(t, DefaultApp("test"), nil, "register", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://joinsock.com/terms/", "https://joinsock.com/privacy/", "2026-10-11"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help lacks %s:\n%s", want, out)
		}
	}
}

func TestRegisterStaleTerms(t *testing.T) {
	srv := socktest.New(t)
	srv.Fail = map[string]error{"RegisterWithApple": socktest.Error(connect.CodeFailedPrecondition, sockv1.ErrorReason_ERROR_REASON_TERMS_NOT_ACCEPTED)}
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	_, _, err := run(t, app, nil, "register", "--handle", "guy", "--name", "Guy", "--accept-terms", "2020-01-01")
	if err == nil || !strings.Contains(err.Error(), "update sock") {
		t.Fatalf("got %v", err)
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

// A machine key the file already holds is never replaced by a login.
func TestLoginKeepsAnExistingMachineKey(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	writeConfig(t, app, "machine_key = \"sock_mk_existing_secret\"\n")
	if _, errOut, err := run(t, app, nil, "login"); err != nil {
		t.Fatalf("login: %v, stderr %q", err, errOut)
	}
	if fc, _ := client.LoadFileConfig(app.ConfigPath); fc.MachineKey != "sock_mk_existing_secret" {
		t.Fatalf("machine_key replaced: %+v", fc)
	}
	if n := len(srv.CallsTo("MintMachineKey")); n != 0 {
		t.Fatalf("%d machine keys minted though the file had one", n)
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
