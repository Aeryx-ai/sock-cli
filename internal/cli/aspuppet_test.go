package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "owner-dm #1") || !strings.HasSuffix(out, ": "+text+"\n") {
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
