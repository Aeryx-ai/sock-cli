package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"

	"github.com/aeryx-ai/sock-cli/internal/socktest"
)

// pruneHub is a signed-in human whose puppets are one of each kind prune
// tells apart, and whose config file holds machineKey for url ("" for none).
func pruneHub(t *testing.T, fileURL func(srv *socktest.Server) string, machineKey string) (*App, *socktest.Server) {
	t.Helper()
	app, srv := signedIn(t)
	with := func(pw *sockv1.PuppetWithDevices, f func(*sockv1.Puppet)) *sockv1.PuppetWithDevices {
		f(pw.GetPuppet().GetPuppet())
		return pw
	}
	bare := func(id, name string) *sockv1.PuppetWithDevices {
		return &sockv1.PuppetWithDevices{Puppet: socktest.Puppet(id, name)}
	}
	srv.Puppets = []*sockv1.PuppetWithDevices{
		bare("p-old", "proj on mac"),
		{Puppet: socktest.Puppet("p-tok", "tok on mac"), Devices: []*sockv1.Device{socktest.Device("d-tok", "p-tok", "sock-mcp")}},
		bare("p-mk", "mine on mac"),
		with(bare("p-sys", "Moderation on hub"), func(p *sockv1.Puppet) { p.System = true }),
		with(bare("p-conn", "claude on web"), func(p *sockv1.Puppet) { p.Connector = &sockv1.Connector{ClientName: "Claude"} }),
		bare("p-ci", "ci"),
	}
	srv.MachineKeyPuppets = []*sockv1.PuppetWithDevices{bare("p-mk", "mine on mac")}
	if machineKey != "" {
		body := fmt.Sprintf("url = %q\nmachine_key = %q\n", fileURL(srv), machineKey)
		if err := os.MkdirAll(filepath.Dir(app.ConfigPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(app.ConfigPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return app, srv
}

func thisServer(srv *socktest.Server) string { return srv.URL }

func erasedIDs(srv *socktest.Server) []string {
	var out []string
	for _, c := range srv.CallsTo("ErasePuppet") {
		out = append(out, c.Msg.(*sockv1.ErasePuppetRequest).GetPuppetId())
	}
	return out
}

// only a project-named puppet no machine key of this machine lists, not a
// system or connector puppet, and with no token unless --with-tokens; typed
// yes erases it
func TestPuppetsPruneErasesAfterConfirmation(t *testing.T) {
	app, srv := pruneHub(t, thisServer, socktest.MachineKeyToken)
	app.StdinIsTTY = func() bool { return true }
	out, errOut, err := run(t, app, strings.NewReader("yes\n"), "puppets", "prune")
	if err != nil {
		t.Fatalf("%v (stderr %q)", err, errOut)
	}
	if got := erasedIDs(srv); !slices.Equal(got, []string{"p-old"}) {
		t.Fatalf("erased %v", got)
	}
	if !strings.Contains(out, "proj on mac") || !strings.HasSuffix(out, "erased p-old\n") || strings.Contains(out, "p-tok") {
		t.Fatalf("stdout %q", out)
	}
	if !strings.Contains(errOut, "Type yes") || !strings.Contains(errOut, "--with-tokens") {
		t.Fatalf("stderr %q", errOut)
	}
	// the machine key is asked, never sent anywhere but its own server
	lists := srv.CallsTo("ListPuppets")
	if len(lists) != 2 || lists[1].Header.Get("Authorization") != "Bearer "+socktest.MachineKeyToken {
		t.Fatalf("listed %d times", len(lists))
	}
}

func TestPuppetsPruneWithTokensAndYes(t *testing.T) {
	app, srv := pruneHub(t, thisServer, socktest.MachineKeyToken)
	mustRun(t, app, "puppets", "prune", "--yes", "--with-tokens")
	if got := erasedIDs(srv); !slices.Equal(got, []string{"p-old", "p-tok"}) {
		t.Fatalf("erased %v", got)
	}
}

func TestPuppetsPruneErasesNothingUnconfirmed(t *testing.T) {
	t.Run("declined", func(t *testing.T) {
		app, srv := pruneHub(t, thisServer, socktest.MachineKeyToken)
		app.StdinIsTTY = func() bool { return true }
		if _, _, err := run(t, app, strings.NewReader("y\n"), "puppets", "prune"); err == nil || err.Error() != errNotConfirmed.Error() {
			t.Fatalf("err = %v", err)
		}
		if got := erasedIDs(srv); len(got) != 0 {
			t.Fatalf("erased %v", got)
		}
	})
	t.Run("off a terminal without --yes", func(t *testing.T) {
		app, srv := pruneHub(t, thisServer, socktest.MachineKeyToken)
		out, _, err := run(t, app, nil, "puppets", "prune")
		if err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(out, "proj on mac") {
			t.Fatalf("stdout %q does not list the candidate", out)
		}
		if got := erasedIDs(srv); len(got) != 0 {
			t.Fatalf("erased %v", got)
		}
	})
}

// a machine key for another server is never sent to this one, so its
// puppets cannot be told apart; a revoked key lists nothing it made
func TestPuppetsPruneMachineKeyItCannotAsk(t *testing.T) {
	for name, tc := range map[string]struct {
		url func(*socktest.Server) string
		key string
	}{
		"another server": {func(*socktest.Server) string { return "https://other.example" }, socktest.MachineKeyToken},
		"revoked key":    {thisServer, "sock_mk_revoked_secret"},
		"no key":         {thisServer, ""},
	} {
		t.Run(name, func(t *testing.T) {
			app, srv := pruneHub(t, tc.url, tc.key)
			mustRun(t, app, "puppets", "prune", "--yes")
			if got := erasedIDs(srv); !slices.Equal(got, []string{"p-old", "p-mk"}) {
				t.Fatalf("erased %v", got)
			}
			for _, c := range srv.CallsTo("ListPuppets") {
				// the fake refuses a revoked key before recording the call
				if a := c.Header.Get("Authorization"); a != "Bearer "+socktest.HumanToken {
					t.Fatalf("listed as %q", a)
				}
			}
		})
	}
}

func TestPuppetsPruneNothingToDo(t *testing.T) {
	app, srv := signedIn(t)
	srv.Puppets = []*sockv1.PuppetWithDevices{{Puppet: socktest.Puppet("p-ci", "ci")}}
	out, errOut := mustRun(t, app, "puppets", "prune")
	if out != "" || !strings.Contains(errOut, "no project puppets to prune") {
		t.Fatalf("stdout %q stderr %q", out, errOut)
	}
}

// one failure does not stop the rest; a puppet already gone counts as erased
func TestPuppetsPruneKeepsGoingAndReportsFailures(t *testing.T) {
	app, srv := pruneHub(t, thisServer, "")
	srv.Fail = map[string]error{"ErasePuppet": socktest.Error(connect.CodeNotFound, sockv1.ErrorReason_ERROR_REASON_NOT_FOUND)}
	if _, errOut, err := run(t, app, nil, "puppets", "prune", "--yes"); err != nil {
		t.Fatalf("%v (stderr %q)", err, errOut)
	}
	srv.Fail = map[string]error{"ErasePuppet": socktest.Unavailable()}
	_, _, err := run(t, app, nil, "puppets", "prune", "--yes")
	if err == nil || !strings.Contains(err.Error(), "2 of 2") {
		t.Fatalf("err = %v", err)
	}
}

// a machine key lists only its own puppets: prune needs the human's view
func TestPuppetsPruneNeedsASession(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, map[string]string{"SOCK_TOKEN": socktest.MachineKeyToken})
	if _, _, err := run(t, app, nil, "puppets", "prune", "--yes"); err == nil || !strings.Contains(err.Error(), "sock login") {
		t.Fatalf("err = %v", err)
	}
	if len(srv.Calls) != 0 {
		t.Fatalf("called %v", srv.Calls)
	}
}

// review: the key's server spelled another way is still its server, and a
// key that cannot be asked is said, and refuses --with-tokens, which would
// reach its live puppets
func TestPuppetsPruneMachineKeyURLSpelling(t *testing.T) {
	app, srv := pruneHub(t, func(srv *socktest.Server) string { return strings.ToUpper(srv.URL[:4]) + srv.URL[4:] + "/" }, socktest.MachineKeyToken)
	mustRun(t, app, "puppets", "prune", "--yes", "--with-tokens")
	if got := erasedIDs(srv); !slices.Equal(got, []string{"p-old", "p-tok"}) {
		t.Fatalf("erased %v", got)
	}
}

func TestPuppetsPruneUnaskedKeyRefusesWithTokens(t *testing.T) {
	app, srv := pruneHub(t, func(*socktest.Server) string { return "https://other.example" }, socktest.MachineKeyToken)
	_, _, err := run(t, app, nil, "puppets", "prune", "--yes", "--with-tokens")
	if err == nil || !strings.Contains(err.Error(), "--with-tokens") {
		t.Fatalf("err = %v", err)
	}
	if got := erasedIDs(srv); len(got) != 0 {
		t.Fatalf("erased %v", got)
	}
	_, errOut := mustRun(t, app, "puppets", "prune", "--yes")
	if !strings.Contains(errOut, "machine_key") {
		t.Fatalf("stderr %q does not say the key was not asked", errOut)
	}
}
