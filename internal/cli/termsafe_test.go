package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode"

	"google.golang.org/protobuf/types/known/timestamppb"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"
	"github.com/spf13/cobra"

	"github.com/guygrigsby/sock-cli/internal/socktest"
)

const (
	osc52   = "\x1b]52;c;Y3VybCBldmlsIHwgc2g=\x07"
	eraseSc = "\x1b[2J\x1b[H"
	rlo     = "\u202e"
	evil    = osc52 + eraseSc + rlo + "\u2066\u200f\u061c"
)

// shown fails t when out could drive or reorder the terminal.
func shown(t *testing.T, what, out string) {
	t.Helper()
	if i := strings.IndexFunc(out, func(r rune) bool {
		return (unicode.IsControl(r) && r != '\n' && r != '\t') || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f || r == 0x061c
	}); i >= 0 {
		t.Errorf("%s: control at %d reaches the terminal: %q", what, i, out)
	}
}

func TestPrinterHidesBidiControls(t *testing.T) {
	var b bytes.Buffer
	p := Printer{}
	ts := timestamppb.Now()
	_ = p.Message(&b, &sockv1.Message{SentAt: ts, Body: &sockv1.Message_Text{Text: "a" + evil + "\nb"}})
	_ = p.Account(&b, &sockv1.Account{DisplayName: evil, Kind: &sockv1.Account_Puppet{Puppet: &sockv1.Puppet{Name: evil}}})
	_ = p.Device(&b, &sockv1.Device{Label: evil, CreatedAt: ts}, false, "", sockv1.DeviceKind_DEVICE_KIND_SESSION)
	shown(t, "printer", b.String())
}

func TestDiagnosticsHideControls(t *testing.T) {
	var b bytes.Buffer
	warn(&b, "server said "+evil)
	l := &listener{errOut: &b}
	l.logf("message %s: %v", "m"+evil, errors.New("hub: "+evil))
	Report(&b, errors.New("permission denied: "+evil))
	shown(t, "diagnostics", b.String())
	if strings.Count(b.String(), "\n") != 3 {
		t.Fatalf("lines: %q", b.String())
	}
}

func TestLoginAndTermsHideControls(t *testing.T) {
	srv := socktest.New(t)
	srv.Session.Account.Kind = &sockv1.Account_Human{Human: &sockv1.Human{Handle: "guy" + evil}}
	srv.Session.Device.Id = "d" + evil
	app := humanApp(t, srv, nil)
	withBrowser(t, app, srv)
	out, errOut, err := run(t, app, nil, "login")
	if err != nil {
		t.Fatalf("login: %v %q", err, errOut)
	}
	shown(t, "login", out+errOut)
	srv.Me = socktest.Human()
	srv.CurrentTerms, srv.AcceptedTerms = "2026"+evil, "2025"+evil
	for _, args := range [][]string{{"terms"}, {"whoami"}} {
		out, errOut, err := run(t, app, nil, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		shown(t, args[0], out+errOut)
	}
}

func TestTokenAndPromptHideControls(t *testing.T) {
	var out, errOut bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := printToken(cmd, "sock_pt_d1_x", "d"+evil); err != nil {
		t.Fatal(err)
	}
	shown(t, "printToken", out.String()+errOut.String())

	srv := socktest.New(t)
	srv.Me = socktest.Human()
	srv.Me.Kind = &sockv1.Account_Human{Human: &sockv1.Human{Handle: "guy" + evil}}
	app := humanApp(t, srv, map[string]string{"SOCK_TOKEN": socktest.HumanToken})
	app.StdinIsTTY = func() bool { return true }
	_, errStr, _ := run(t, app, strings.NewReader("no\n"), "account", "erase")
	shown(t, "erase prompt", errStr)
}
