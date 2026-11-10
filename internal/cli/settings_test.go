package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/aeryx-ai/sock-cli/internal/config"
	"github.com/aeryx-ai/sock-cli/internal/socktest"
	client "github.com/aeryx-ai/sock-client"
)

type settingsJSON struct {
	Config  string `json:"config"`
	URL     string `json:"url"`
	Session struct {
		Check, Handle, Reason string
	} `json:"session"`
	MachineKey struct {
		Check, Reason string
	} `json:"machine_key"`
	PuppetToken  string `json:"puppet_token"`
	MessageOwner struct {
		Value, From string
	} `json:"message_owner"`
}

func settingsOf(t *testing.T, out string) settingsJSON {
	t.Helper()
	var s settingsJSON
	if err := json.Unmarshal([]byte(out), &s); err != nil || strings.Count(out, "\n") != 1 {
		t.Fatalf("json %q: %v", out, err)
	}
	return s
}

// No file: everything is missing or default, and nothing reaches a server.
func TestSettingsWithoutAFile(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	out, _, err := run(t, app, nil, "settings")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("config         %s\nurl            %s\nsession        missing\nmachine_key    missing\npuppet_token   absent\nmessage_owner  on (default)\n", app.ConfigPath, config.DefaultURL)
	if out != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
	if len(srv.Calls) != 0 {
		t.Fatalf("%d calls", len(srv.Calls))
	}
}

// Every check is made with the file's credentials at the file's server,
// whatever SOCK_URL says, and no credential is printed.
func TestSettingsReportsTheFile(t *testing.T) {
	srv := socktest.New(t)
	srv.Me = socktest.Human()
	app := humanApp(t, srv, map[string]string{"SOCK_URL": "http://127.0.0.1:1"})
	writeConfig(t, app, fmt.Sprintf("url = %q\ntoken = %q\nmachine_key = %q\npuppet_token = %q\nmessage_owner = false\n", srv.URL, socktest.HumanToken, socktest.MachineKeyToken, socktest.Token))
	out, errOut, err := run(t, app, nil, "settings")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("config         %s\nurl            %s\nsession        valid @guy\nmachine_key    valid\npuppet_token   present\nmessage_owner  off (file)\n", app.ConfigPath, srv.URL)
	if out != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
	for _, secret := range []string{socktest.HumanToken, socktest.MachineKeyToken, socktest.Token, "_secret"} {
		if strings.Contains(out+errOut, secret) {
			t.Fatalf("%s printed", secret)
		}
	}
	if got := srv.CallsTo("GetMe")[0].Header.Get("Authorization"); got != "Bearer "+socktest.HumanToken {
		t.Fatalf("session checked with %q", got)
	}
	if got := srv.CallsTo("ListPuppets")[0].Header.Get("Authorization"); got != "Bearer "+socktest.MachineKeyToken {
		t.Fatalf("machine key checked with %q", got)
	}
	if n := hubWrites(srv); n != 0 {
		t.Fatalf("%d hub writes", n)
	}

	out, _, err = run(t, app, nil, "--json", "settings")
	if err != nil {
		t.Fatal(err)
	}
	s := settingsOf(t, out)
	if s.Config != app.ConfigPath || s.URL != srv.URL || s.Session.Check != "valid" || s.Session.Handle != "guy" || s.MachineKey.Check != "valid" ||
		s.PuppetToken != "present" || s.MessageOwner.Value != "off" || s.MessageOwner.From != "file" {
		t.Fatalf("json %+v", s)
	}
}

// A report exits 0 whatever the checks answer.
func TestSettingsReportsRejectedAndUnchecked(t *testing.T) {
	srv := socktest.New(t)
	srv.Fail = map[string]error{"ListPuppets": socktest.Unavailable()}
	app := humanApp(t, srv, nil)
	writeConfig(t, app, fmt.Sprintf("url = %q\ntoken = \"sock_st_dead_secret\"\nmachine_key = %q\n", srv.URL, socktest.MachineKeyToken))
	out, _, err := run(t, app, nil, "--json", "settings")
	if err != nil {
		t.Fatal(err)
	}
	s := settingsOf(t, out)
	if s.Session.Check != "rejected" || s.Session.Handle != "" || s.MachineKey.Check != "unchecked" || s.MachineKey.Reason == "" || s.PuppetToken != "absent" {
		t.Fatalf("json %+v", s)
	}
	out, _, err = run(t, app, nil, "settings")
	if err != nil || !strings.Contains(out, "session        rejected\n") || !strings.Contains(out, "machine_key    unchecked (") {
		t.Fatalf("text %q, %v", out, err)
	}
}

func TestSettingsNamesTheEnvOverride(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, map[string]string{client.MessageOwnerEnvVar: "0"})
	writeConfig(t, app, "message_owner = true\n")
	out, _, err := run(t, app, nil, "--json", "settings")
	if err != nil {
		t.Fatal(err)
	}
	if s := settingsOf(t, out); s.MessageOwner.Value != "off" || s.MessageOwner.From != "SOCK_MCP_MESSAGE_OWNER" {
		t.Fatalf("json %+v", s)
	}
}

func TestSettingsWritesMessageOwner(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	for _, tc := range []struct {
		arg  string
		want bool
	}{{"off", false}, {"on", true}} {
		out, _, err := run(t, app, nil, "settings", "--message-owner="+tc.arg)
		if err != nil || !strings.Contains(out, "message_owner  "+tc.arg+" (file)\n") {
			t.Fatalf("%s: %q, %v", tc.arg, out, err)
		}
		if fc, _ := client.LoadFileConfig(app.ConfigPath); fc.MessageOwner == nil || *fc.MessageOwner != tc.want {
			t.Fatalf("%s: config %+v", tc.arg, fc)
		}
	}
	if _, _, err := run(t, app, nil, "settings", "--message-owner=maybe"); err == nil {
		t.Fatal("--message-owner=maybe accepted")
	}
}

func TestSettingsWritesURLOnlyWithoutCredentials(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	out, _, err := run(t, app, nil, "settings", "--url", "https://hub.example")
	if err != nil || !strings.Contains(out, "url            https://hub.example\n") {
		t.Fatalf("%q, %v", out, err)
	}
	for _, key := range []string{"token", "puppet_token", "machine_key"} {
		body := fmt.Sprintf("url = %q\n%s = \"sock_xx_secret\"\n", srv.URL, key)
		writeConfig(t, app, body)
		_, _, err := run(t, app, nil, "settings", "--url", "https://hub.example", "--message-owner=off")
		if err == nil || err.Error() != "sign out first (sock logout); the credentials here belong to "+srv.URL {
			t.Fatalf("%s: %v", key, err)
		}
		if b, _ := os.ReadFile(app.ConfigPath); string(b) != body {
			t.Fatalf("%s: file changed", key)
		}
	}
}
