package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/guygrigsby/sock-cli/internal/socktest"
)

// A session token from the file goes only to the file's server, logout too.
func TestFileTokenIsNotSentToTheEnvironmentsURL(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, map[string]string{"SOCK_TOKEN": "sock_st_env_other"})
	writeConfig(t, app, fmt.Sprintf("url = %q\ntoken = %q\n", "https://file.example", socktest.HumanToken))
	if _, _, err := run(t, app, nil, "logout"); err == nil || strings.Contains(err.Error(), socktest.HumanToken) {
		t.Fatalf("logout: %v", err)
	}
	if len(srv.CallsTo("SignOut")) != 0 || fileToken(t, app) != socktest.HumanToken {
		t.Fatal("file token sent to SOCK_URL")
	}
	app = humanApp(t, srv, nil)
	writeConfig(t, app, fmt.Sprintf("url = %q\ntoken = %q\n", "https://file.example", socktest.HumanToken))
	if _, _, err := run(t, app, nil, "whoami"); err == nil {
		t.Fatal("whoami sent the file token to SOCK_URL")
	}
	if len(srv.CallsTo("WhoAmI")) != 0 {
		t.Fatal("file token sent to SOCK_URL")
	}
}

// logout signs out the file's session at the file's server even when
// SOCK_TOKEN names another credential.
func TestLogoutUsesTheFileTokenAndServer(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, map[string]string{"SOCK_TOKEN": "sock_st_env_other"})
	writeConfig(t, app, fmt.Sprintf("url = %q\ntoken = %q\n", srv.URL, socktest.HumanToken))
	if out, _, err := run(t, app, nil, "logout"); err != nil || out != "signed out\n" {
		t.Fatalf("logout: %q, %v", out, err)
	}
	calls := srv.CallsTo("SignOut")
	if len(calls) != 1 || calls[0].Header.Get("Authorization") != "Bearer "+socktest.HumanToken {
		t.Fatalf("SignOut calls %v", calls)
	}
}

// sock login at SOCK_URL records that server beside the token, so the next
// command with the same SOCK_URL runs; a file naming another server is
// refused before the browser opens.
func TestLoginRecordsItsServer(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	if _, errOut, err := run(t, app, nil, "login"); err != nil {
		t.Fatalf("login: %v, %q", err, errOut)
	}
	srv.Me = socktest.Human()
	if _, _, err := run(t, app, nil, "whoami"); err != nil {
		t.Fatalf("whoami after login: %v", err)
	}
	other := humanApp(t, srv, nil)
	writeConfig(t, other, "url = \"https://other.example\"\n")
	if _, _, err := run(t, other, nil, "login"); err == nil {
		t.Fatal("login saved a token for another server")
	}
}

// machine-key use checks the file's server before the key leaves: the key
// and the file's gate never go to a SOCK_URL the file does not name.
func TestMachineKeyUseChecksTheFileServerFirst(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	writeConfig(t, app, "url = \"https://file.example\"\naccess_gate = \"fg\"\n")
	if _, _, err := run(t, app, strings.NewReader(socktest.MachineKeyToken+"\n"), "machine-key", "use"); err == nil {
		t.Fatal("machine-key use saved a key for another server")
	}
	if n := len(srv.CallsTo("ListPuppets")); n != 0 {
		t.Fatalf("%d ListPuppets calls reached SOCK_URL", n)
	}
}
