package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	client "github.com/aeryx-ai/sock-client"
	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"

	"github.com/aeryx-ai/sock-cli/internal/socktest"
)

// signedIn is an App holding the fake's human session token in SOCK_TOKEN,
// talking to a fake whose caller is that human.
func signedIn(t *testing.T) (*App, *socktest.Server) {
	t.Helper()
	srv := socktest.New(t)
	srv.Me = socktest.Human()
	srv.Accounts = []*sockv1.Account{socktest.Human(), {Id: "human-2", DisplayName: "Ann", Kind: &sockv1.Account_Human{Human: &sockv1.Human{Handle: "ann"}}}}
	return humanApp(t, srv, map[string]string{"SOCK_TOKEN": socktest.HumanToken}), srv
}

func lastRequest[T any](t *testing.T, srv *socktest.Server, method string) T {
	t.Helper()
	calls := srv.CallsTo(method)
	if len(calls) == 0 {
		t.Fatalf("no %s call", method)
	}
	return calls[len(calls)-1].Msg.(T)
}

func mustRun(t *testing.T, app *App, args ...string) (string, string) {
	t.Helper()
	out, errOut, err := run(t, app, nil, args...)
	if err != nil {
		t.Fatalf("sock %s: %v (stderr %q)", strings.Join(args, " "), err, errOut)
	}
	return out, errOut
}

var fixed = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC).Local().Format(time.DateTime)

func TestWhoamiHumanShowsTerms(t *testing.T) {
	app, srv := signedIn(t)
	srv.AcceptedTerms = ""
	out, _ := mustRun(t, app, "whoami")
	if out != "Guy  human @guy  id human-1\nterms accepted none, current 2026-10-11\n" {
		t.Fatalf("got %q", out)
	}
}

func TestTerms(t *testing.T) {
	app, srv := signedIn(t)
	srv.AcceptedTerms = "2026-01-01"
	if out, _ := mustRun(t, app, "terms"); out != "accepted 2026-01-01, current 2026-10-11\nhttps://joinsock.com/terms/\n" {
		t.Fatalf("terms: %q", out)
	}
	if out, _ := mustRun(t, app, "terms", "accept", "2026-10-11"); out != "accepted terms 2026-10-11\n" {
		t.Fatalf("accept: %q", out)
	}
	if v := lastRequest[*sockv1.AcceptTermsRequest](t, srv, "AcceptTerms").GetTermsVersion(); v != "2026-10-11" {
		t.Fatalf("version %q", v)
	}
}

func TestHumanErrorsNameTheCommand(t *testing.T) {
	cases := map[sockv1.ErrorReason]string{
		sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID:      "run sock login",
		sockv1.ErrorReason_ERROR_REASON_TERMS_NOT_ACCEPTED: "sock terms accept",
	}
	for reason, want := range cases {
		app, srv := signedIn(t)
		srv.Fail = map[string]error{"ChangeHandle": socktest.Error(connect.CodeFailedPrecondition, reason)}
		if _, _, err := run(t, app, nil, "handle", "set", "x"); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v: got %v", reason, err)
		}
	}
}

func TestPuppetTokenErrorsKeepPuppetWording(t *testing.T) {
	srv := socktest.New(t)
	srv.Fail = map[string]error{"GetMe": socktest.Error(connect.CodeUnauthenticated, sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID)}
	_, _, err := run(t, testApp(t, srv), nil, "whoami")
	if err == nil || strings.Contains(err.Error(), "sock login") {
		t.Fatalf("got %v", err)
	}
}

func TestAccessGateSentAndNeverPrinted(t *testing.T) {
	srv := socktest.New(t)
	srv.Fail = map[string]error{"GetMe": socktest.Unavailable()}
	app := humanApp(t, srv, map[string]string{"SOCK_TOKEN": socktest.HumanToken, "SOCK_ACCESS_GATE": "gate-secret"})
	_, errOut, err := run(t, app, nil, "whoami")
	if err == nil || strings.Contains(err.Error()+errOut, "gate-secret") {
		t.Fatalf("got %v, stderr %q", err, errOut)
	}
	for _, c := range srv.CallsTo("GetMe") {
		if c.Header.Get("Sock-Access-Gate") != "gate-secret" {
			t.Fatalf("gate missing on %v", c.Header)
		}
	}
}

func TestProfileCommands(t *testing.T) {
	app, srv := signedIn(t)
	if out, _ := mustRun(t, app, "name", "set", "Guy", "G"); out != "Guy G  human @guy  id human-1\n" {
		t.Fatalf("name set: %q", out)
	}
	mustRun(t, app, "handle", "set", "@guyg")
	if h := lastRequest[*sockv1.ChangeHandleRequest](t, srv, "ChangeHandle").GetHandle(); h != "guyg" {
		t.Fatalf("handle %q", h)
	}
	if out, _ := mustRun(t, app, "accounts", "get", "human-2", "nope"); out != "Ann  human @ann  id human-2\n" {
		t.Fatalf("accounts get: %q", out)
	}
	if out, _ := mustRun(t, app, "accounts", "lookup", "@ann"); out != "Ann  human @ann  id human-2\n" {
		t.Fatalf("accounts lookup: %q", out)
	}
	ids := make([]string, 101)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
	}
	if _, _, err := run(t, app, nil, append([]string{"accounts", "get"}, ids...)...); err == nil {
		t.Fatal("101 ids accepted")
	}
}

func TestDevices(t *testing.T) {
	app, srv := signedIn(t)
	mk := socktest.Device("d-mk", "human-1", "build-7")
	mk.Kind = sockv1.DeviceKind_DEVICE_KIND_MACHINE_KEY
	phone := socktest.Device("d-2", "human-1", "iPhone")
	phone.Kind = sockv1.DeviceKind_DEVICE_KIND_SESSION
	// the first is unset, as from a server older than device kinds: a human's device is a session
	srv.Devices = []*sockv1.Device{socktest.Device(socktest.HumanDeviceID, "human-1", "sock CLI on box"), phone, mk}
	out, _ := mustRun(t, app, "devices")
	want := socktest.HumanDeviceID + "  session  sock CLI on box  created " + fixed + "  (this)\n" +
		"d-2  session  iPhone  created " + fixed + "\n" +
		"d-mk  machine-key  build-7  created " + fixed + "\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
	if out, _ := mustRun(t, app, "devices", "revoke", "d-2"); out != "revoked d-2\n" {
		t.Fatalf("revoke: %q", out)
	}
	if id := lastRequest[*sockv1.RevokeDeviceRequest](t, srv, "RevokeDevice").GetDeviceId(); id != "d-2" {
		t.Fatalf("revoked %q", id)
	}
	out, _ = mustRun(t, app, "--json", "devices")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 3 {
		t.Fatalf("json lines %q", out)
	}
}

func TestPuppets(t *testing.T) {
	app, srv := signedIn(t)
	srv.Puppets = []*sockv1.PuppetWithDevices{{Puppet: socktest.Puppet("p-1", "ci"), Devices: []*sockv1.Device{socktest.Device("pd-1", "p-1", "runner")}}}
	out, _ := mustRun(t, app, "puppets")
	if want := "ci  puppet ci of human-1  id p-1\n  pd-1  puppet-token  runner  created " + fixed + "\n"; out != want {
		t.Fatalf("got %q, want %q", out, want)
	}

	out, errOut := mustRun(t, app, "puppets", "create", "ci", "--token-label", "runner")
	if out != "sock_pt_device-new_secret\n" || !strings.Contains(errOut, "puppet ci of human-1") || !strings.Contains(errOut, "device device-new") {
		t.Fatalf("create: stdout %q stderr %q", out, errOut)
	}
	if req := lastRequest[*sockv1.CreatePuppetRequest](t, srv, "CreatePuppet"); req.GetName() != "ci" || req.GetTokenLabel() != "runner" {
		t.Fatalf("create request %v", req)
	}
	out, _ = mustRun(t, app, "--json", "puppets", "create", "ci", "--token-label", "runner")
	var cr sockv1.CreatePuppetResponse
	if err := protojson.Unmarshal([]byte(out), &cr); err != nil || cr.GetToken() == "" {
		t.Fatalf("create json %q: %v", out, err)
	}
	if _, _, err := run(t, app, nil, "puppets", "create", "ci"); err == nil {
		t.Fatal("create without --token-label accepted")
	}

	out, errOut = mustRun(t, app, "puppets", "token", "p-1", "--label", "laptop")
	if out != "sock_pt_device-minted_secret\n" || errOut != "device device-minted\n" {
		t.Fatalf("token: stdout %q stderr %q", out, errOut)
	}
	if req := lastRequest[*sockv1.MintPuppetTokenRequest](t, srv, "MintPuppetToken"); req.GetPuppetId() != "p-1" || req.GetLabel() != "laptop" {
		t.Fatalf("mint request %v", req)
	}
}

func TestPuppetErase(t *testing.T) {
	app, srv := signedIn(t)
	if _, _, err := run(t, app, nil, "puppets", "erase", "p-1"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("off a terminal without --yes: %v", err)
	}
	app.StdinIsTTY = func() bool { return true }
	if _, _, err := run(t, app, strings.NewReader("no\n"), "puppets", "erase", "p-1"); err == nil {
		t.Fatal("declined erase went ahead")
	}
	if len(srv.CallsTo("ErasePuppet")) != 0 {
		t.Fatal("ErasePuppet called without confirmation")
	}
	if out, _, err := run(t, app, strings.NewReader("yes\n"), "puppets", "erase", "p-1"); err != nil || out != "erased p-1\n" {
		t.Fatalf("confirmed erase: %q, %v", out, err)
	}
	app.StdinIsTTY = func() bool { return false }
	if out, _ := mustRun(t, app, "puppets", "erase", "p-2", "--yes"); out != "erased p-2\n" {
		t.Fatalf("--yes: %q", out)
	}
}

func TestConversationCommands(t *testing.T) {
	app, srv := signedIn(t)
	if out, _ := mustRun(t, app, "conversations", "direct", "@ann"); out != "conv-direct  direct  members human-1,human-2\n" {
		t.Fatalf("direct: %q", out)
	}
	if out, _ := mustRun(t, app, "conversations", "group", "@ann", "p-1"); out != "conv-group  group  members human-1,human-2,p-1\n" {
		t.Fatalf("group: %q", out)
	}
	if out, _ := mustRun(t, app, "conversations", "group"); out != "conv-group  group  members human-1\n" {
		t.Fatalf("empty group: %q", out)
	}
	twenty := make([]string, 20)
	for i := range twenty {
		twenty[i] = fmt.Sprint("a", i)
	}
	if _, _, err := run(t, app, nil, append([]string{"conversations", "group"}, twenty...)...); err == nil {
		t.Fatal("20 others accepted")
	}
	mustRun(t, app, "conversations", "add", "c-1", "@ann")
	if req := lastRequest[*sockv1.AddMemberRequest](t, srv, "AddMember"); req.GetConversationId() != "c-1" || req.GetAccountId() != "human-2" {
		t.Fatalf("add %v", req)
	}
	mustRun(t, app, "conversations", "remove", "c-1", "p-1")
	if req := lastRequest[*sockv1.RemoveMemberRequest](t, srv, "RemoveMember"); req.GetConversationId() != "c-1" || req.GetAccountId() != "p-1" {
		t.Fatalf("remove %v", req)
	}
	if out, _ := mustRun(t, app, "conversations", "leave", "c-1"); out != "left c-1\n" {
		t.Fatalf("leave: %q", out)
	}
	if _, _, err := run(t, app, nil, "conversations", "direct", "@nobody"); err == nil || !strings.Contains(err.Error(), "@nobody") {
		t.Fatalf("unknown handle: %v", err)
	}
}

func TestHumanSendNeedsConversation(t *testing.T) {
	app, srv := signedIn(t)
	if _, _, err := run(t, app, nil, "send", "hi"); err == nil || !strings.Contains(err.Error(), "--conversation") {
		t.Fatalf("got %v", err)
	}
	if len(srv.CallsTo("NotifyOwner")) != 0 {
		t.Fatal("NotifyOwner called for a human")
	}
	mustRun(t, app, "send", "--conversation", "c-1", "hi")
	if req := lastRequest[*sockv1.SendRequest](t, srv, "Send"); req.GetConversationId() != "c-1" || req.GetText() != "hi" {
		t.Fatalf("send %v", req)
	}
}

func TestAck(t *testing.T) {
	app, srv := signedIn(t)
	if out, _ := mustRun(t, app, "ack", "m-1", "m-2"); out != "" {
		t.Fatalf("ack printed %q", out)
	}
	if ids := lastRequest[*sockv1.AckRequest](t, srv, "Ack").GetMessageIds(); strings.Join(ids, ",") != "m-1,m-2" {
		t.Fatalf("acked %v", ids)
	}
	if _, _, err := run(t, app, nil, "ack"); err == nil {
		t.Fatal("ack with no ids accepted")
	}
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
	}
	if _, _, err := run(t, app, nil, append([]string{"ack"}, ids...)...); err == nil {
		t.Fatal("501 ids accepted")
	}
}

func TestBlocks(t *testing.T) {
	app, srv := signedIn(t)
	if out, _ := mustRun(t, app, "block", "@ann"); out != "blocked human-2\n" {
		t.Fatalf("block: %q", out)
	}
	if id := lastRequest[*sockv1.BlockAccountRequest](t, srv, "BlockAccount").GetAccountId(); id != "human-2" {
		t.Fatalf("blocked %q", id)
	}
	if out, _ := mustRun(t, app, "unblock", "human-2"); out != "unblocked human-2\n" {
		t.Fatalf("unblock: %q", out)
	}
	srv.Blocked = []*sockv1.BlockedAccount{{AccountId: "human-2", BlockedAt: timestamppb.New(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))}}
	if out, _ := mustRun(t, app, "blocks"); out != "human-2  blocked "+fixed+"\n" {
		t.Fatalf("blocks: %q", out)
	}
}

// evidenceFile writes n messages as sock fetch --json prints them.
func evidenceFile(t *testing.T, n int, tag []byte) string {
	t.Helper()
	srv := socktest.New(t)
	for i := range n {
		m := socktest.Text(fmt.Sprint("m-", i), "c-1", int64(i+1), "mean words")
		m.SenderId = "human-2"
		m.EvidenceTag = tag
		srv.Pending = append(srv.Pending, m)
	}
	out, _, err := run(t, testApp(t, srv), nil, "--json", "fetch", "--no-ack")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "evidence.jsonl")
	if err := os.WriteFile(p, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReport(t *testing.T) {
	app, srv := signedIn(t)
	tag := []byte("0123456789abcdef0123456789abcdef")
	out, _ := mustRun(t, app, "report", "@ann", "--reason", "harassment", "--note", "see these", "--evidence", evidenceFile(t, 2, tag))
	if out != "filed report report-1\n" {
		t.Fatalf("report: %q", out)
	}
	req := lastRequest[*sockv1.FileReportRequest](t, srv, "FileReport")
	if req.GetSubjectId() != "human-2" || req.GetReason() != sockv1.ReportReason_REPORT_REASON_HARASSMENT || req.GetNote() != "see these" || len(req.GetEvidence()) != 2 {
		t.Fatalf("request %v", req)
	}
	ev := req.GetEvidence()[1]
	if ev.GetMessageId() != "m-1" || ev.GetSeq() != 2 || ev.GetSenderId() != "human-2" || ev.GetText() != "mean words" || string(ev.GetEvidenceTag()) != string(tag) || ev.GetSentAt() == nil {
		t.Fatalf("evidence %v", ev)
	}

	for name, args := range map[string][]string{
		"bad reason":   {"report", "@ann", "--reason", "rude"},
		"no reason":    {"report", "@ann"},
		"too many":     {"report", "@ann", "--reason", "spam", "--evidence", evidenceFile(t, 21, tag)},
		"no tag":       {"report", "@ann", "--reason", "spam", "--evidence", evidenceFile(t, 1, nil)},
		"missing file": {"report", "@ann", "--reason", "spam", "--evidence", filepath.Join(t.TempDir(), "none")},
	} {
		if _, _, err := run(t, app, nil, args...); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if n := len(srv.CallsTo("FileReport")); n != 1 {
		t.Fatalf("%d FileReport calls; refusals reached the server", n)
	}
}

func TestExport(t *testing.T) {
	app, srv := signedIn(t)
	srv.Blocked = []*sockv1.BlockedAccount{{AccountId: "human-2"}}
	out, _ := mustRun(t, app, "export")
	var got struct {
		Identity  json.RawMessage `json:"identity"`
		Messaging json.RawMessage `json:"messaging"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not one JSON object: %q: %v", out, err)
	}
	var id sockv1.ExportIdentityDataResponse
	var msg sockv1.ExportMessagingDataResponse
	if err := protojson.Unmarshal(got.Identity, &id); err != nil || id.GetAccount().GetId() != "human-1" {
		t.Fatalf("identity %s: %v", got.Identity, err)
	}
	if err := protojson.Unmarshal(got.Messaging, &msg); err != nil || len(msg.GetBlockedAccountIds()) != 1 {
		t.Fatalf("messaging %s: %v", got.Messaging, err)
	}
}

func TestAccountErase(t *testing.T) {
	app, srv := signedIn(t)
	if _, _, err := run(t, app, nil, "account", "erase"); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("off a terminal: %v", err)
	}
	if _, _, err := run(t, app, nil, "account", "erase", "--confirm", "ann"); err == nil {
		t.Fatal("wrong handle accepted")
	}
	app.StdinIsTTY = func() bool { return true }
	if _, _, err := run(t, app, strings.NewReader("ann\n"), "account", "erase"); err == nil {
		t.Fatal("wrong typed handle accepted")
	}
	if len(srv.CallsTo("EraseAccount")) != 0 {
		t.Fatal("EraseAccount called without confirmation")
	}
	if out, _, err := run(t, app, strings.NewReader("guy\n"), "account", "erase"); err != nil || out != "erased\n" {
		t.Fatalf("typed handle: %q, %v", out, err)
	}
}

func TestAccountEraseRemovesFileToken(t *testing.T) {
	srv := socktest.New(t)
	srv.Me = socktest.Human()
	app := humanApp(t, srv, nil)
	writeConfig(t, app, fmt.Sprintf("url = %q\ntoken = %q\n", srv.URL, socktest.HumanToken))
	if out, _ := mustRun(t, app, "account", "erase", "--confirm", "@guy"); out != "erased\n" {
		t.Fatalf("got %q", out)
	}
	if fileToken(t, app) != "" {
		t.Fatal("token kept after erase")
	}
}

// `puppets use` keeps a puppet token where sock-mcp reads it, without printing
// it or touching the human's own token.
func TestPuppetsUseWritesThePuppetTokenToTheConfigFile(t *testing.T) {
	app, srv := signedIn(t)
	app.ConfigPath = filepath.Join(t.TempDir(), "sock", "config.toml")
	if err := os.MkdirAll(filepath.Dir(app.ConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app.ConfigPath, []byte("token = \"sock_st_human_secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut := mustRun(t, app, "puppets", "use", "p-1")
	if strings.Contains(out+errOut, "sock_pt_") {
		t.Fatalf("the token was printed: stdout %q stderr %q", out, errOut)
	}
	if req := lastRequest[*sockv1.MintPuppetTokenRequest](t, srv, "MintPuppetToken"); req.GetPuppetId() != "p-1" || req.GetLabel() == "" {
		t.Fatalf("mint request %v", req)
	}
	f, err := client.LoadFileConfig(app.ConfigPath)
	if err != nil || f.PuppetToken != "sock_pt_device-minted_secret" || f.Token != "sock_st_human_secret" || f.URL != app.Getenv("SOCK_URL") {
		t.Fatalf("file %+v, %v", f, err)
	}
	fi, err := os.Stat(app.ConfigPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, %v", fi, err)
	}
	// a second use replaces the first
	mustRun(t, app, "puppets", "use", "p-1")
	if f, _ := client.LoadFileConfig(app.ConfigPath); f.PuppetToken == "" {
		t.Fatal("second use lost the token")
	}
}

// The token is saved with the URL (and gate) it was minted against: sock-mcp
// reads them from the file, so a staging token never goes to the default hub.
func TestPuppetsUseRefusesAFileURLThatIsNotTheMintingURL(t *testing.T) {
	app, srv := signedIn(t)
	app.ConfigPath = filepath.Join(t.TempDir(), "sock", "config.toml")
	if err := os.MkdirAll(filepath.Dir(app.ConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "url = \"https://other.example\"\ntoken = \"sock_st_human_secret\"\n"
	if err := os.WriteFile(app.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, app, nil, "puppets", "use", "p-1")
	if err == nil || !strings.Contains(err.Error(), "other.example") && !strings.Contains(err.Error(), "url") {
		t.Fatalf("err = %v", err)
	}
	if n := len(srv.CallsTo("MintPuppetToken")); n != 0 {
		t.Fatalf("minted %d tokens before refusing", n)
	}
	if b, _ := os.ReadFile(app.ConfigPath); string(b) != body {
		t.Fatalf("file changed: %q", b)
	}
}

func TestPuppetsUseSavesTheMintingGate(t *testing.T) {
	srv := socktest.New(t)
	srv.Me = socktest.Human()
	app := humanApp(t, srv, map[string]string{"SOCK_TOKEN": socktest.HumanToken, "SOCK_ACCESS_GATE": "gate-1"})
	mustRun(t, app, "puppets", "use", "p-1")
	f, err := client.LoadFileConfig(app.ConfigPath)
	if err != nil || f.AccessGate != "gate-1" || f.URL != srv.URL {
		t.Fatalf("file %+v, %v", f, err)
	}
}

func TestMachineKeyCreatePrintsTheKeyAlone(t *testing.T) {
	app, srv := signedIn(t)
	out, errOut := mustRun(t, app, "machine-key", "create", "--label", "mac")
	if out != "sock_mk_device-mk_secret\n" || !strings.Contains(errOut, "device device-mk") {
		t.Fatalf("stdout %q stderr %q", out, errOut)
	}
	if req := lastRequest[*sockv1.MintMachineKeyRequest](t, srv, "MintMachineKey"); req.GetLabel() != "mac" {
		t.Fatalf("request %v", req)
	}
}

// The label defaults to the short hostname, cut to 64 characters (cli.md).
func TestMachineKeyCreateLabelDefaultsToTheShortHostname(t *testing.T) {
	app, srv := signedIn(t)
	app.Hostname = func() (string, error) { return "build-7.example.com", nil }
	mustRun(t, app, "machine-key", "create")
	if req := lastRequest[*sockv1.MintMachineKeyRequest](t, srv, "MintMachineKey"); req.GetLabel() != "build-7" {
		t.Fatalf("label %q", req.GetLabel())
	}
	app.Hostname = func() (string, error) { return strings.Repeat("é", 80), nil }
	mustRun(t, app, "machine-key", "create")
	if req := lastRequest[*sockv1.MintMachineKeyRequest](t, srv, "MintMachineKey"); req.GetLabel() != strings.Repeat("é", 64) {
		t.Fatalf("label %q", req.GetLabel())
	}
	if _, _, err := run(t, app, nil, "machine-key", "create", "--label", strings.Repeat("x", 65)); err == nil {
		t.Fatal("a 65-character --label accepted")
	}
}

func ensureApp(t *testing.T, srv *socktest.Server, token string) (*App, string) {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	return humanApp(t, srv, map[string]string{"SOCK_TOKEN": token, "XDG_STATE_HOME": state, "HOME": t.TempDir()}), state
}

// `puppets ensure` makes the project's puppet and prints where its token is
// kept, never the token: the hook reads the file.
func TestPuppetsEnsureMintsOnceAndPrintsOnlyThePath(t *testing.T) {
	srv := socktest.New(t)
	app, state := ensureApp(t, srv, socktest.MachineKeyToken)
	dir := filepath.Join(t.TempDir(), "myproj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, errOut := mustRun(t, app, "puppets", "ensure", dir)
	path := strings.TrimSpace(out)
	if strings.Contains(out+errOut, "sock_pt_") || !strings.HasPrefix(path, filepath.Join(state, "sock-integrations", "puppets")) {
		t.Fatalf("stdout %q stderr %q", out, errOut)
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) != "sock_pt_device-new_secret" {
		t.Fatalf("token file %q: %v", b, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	creates := srv.CallsTo("CreatePuppet")
	if len(creates) != 1 || !strings.HasPrefix(creates[0].Msg.(*sockv1.CreatePuppetRequest).GetName(), "myproj on ") {
		t.Fatalf("creates %v", creates)
	}
	if got := creates[0].Header.Get("Authorization"); got != "Bearer "+socktest.MachineKeyToken {
		t.Fatalf("authorization %q", got)
	}
	if out2, _ := mustRun(t, app, "puppets", "ensure", dir); out2 != out || len(srv.CallsTo("CreatePuppet")) != 1 {
		t.Fatalf("second ensure printed %q, creates %d", out2, len(srv.CallsTo("CreatePuppet")))
	}
}

func TestPuppetsEnsureNeedsAMachineKey(t *testing.T) {
	for _, tok := range []string{socktest.HumanToken, socktest.Token} {
		srv := socktest.New(t)
		app, _ := ensureApp(t, srv, tok)
		_, _, err := run(t, app, nil, "puppets", "ensure", t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "machine key") {
			t.Fatalf("token %s...: %v", tok[:8], err)
		}
		if len(srv.CallsTo("CreatePuppet")) != 0 {
			t.Fatal("minted with a token that is not a machine key")
		}
	}
}

// security review 1: a machine key's puppets on one server are not another
// server's: ensure against B neither offers B the token cached for A nor
// skips making B's puppet
func TestPuppetsEnsureKeepsEachServersPuppetsApart(t *testing.T) {
	a, b := socktest.New(t), socktest.New(t)
	appA, state := ensureApp(t, a, socktest.MachineKeyToken)
	appB := humanApp(t, b, map[string]string{"SOCK_TOKEN": socktest.MachineKeyToken, "XDG_STATE_HOME": state, "HOME": t.TempDir()})
	dir := filepath.Join(t.TempDir(), "myproj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outA, _ := mustRun(t, appA, "puppets", "ensure", dir)
	outB, _ := mustRun(t, appB, "puppets", "ensure", dir)
	if outA == outB || len(b.CallsTo("GetMe")) != 0 || len(b.CallsTo("CreatePuppet")) != 1 {
		t.Fatalf("a %q b %q; b's GetMe %d CreatePuppet %d", outA, outB, len(b.CallsTo("GetMe")), len(b.CallsTo("CreatePuppet")))
	}
}

// `machine-key use` keeps a machine key where sock-mcp reads it.
// It comes on stdin, never argv (visible in ps), and is never printed.
func TestMachineKeyUseSavesTheKeyFromStdin(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	if err := os.MkdirAll(filepath.Dir(app.ConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app.ConfigPath, []byte("token = \"sock_st_human_secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := run(t, app, strings.NewReader(socktest.MachineKeyToken+"\n"), "machine-key", "use")
	if err != nil || strings.Contains(out+errOut, "sock_mk_") {
		t.Fatalf("err %v, stdout %q stderr %q", err, out, errOut)
	}
	f, err := client.LoadFileConfig(app.ConfigPath)
	if err != nil || f.MachineKey != socktest.MachineKeyToken || f.Token != "sock_st_human_secret" || f.URL != srv.URL {
		t.Fatalf("file %+v, %v", f, err)
	}
	if fi, _ := os.Stat(app.ConfigPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	// the key was checked against the hub it is saved with
	if req := srv.CallsTo("ListPuppets"); len(req) != 1 || req[0].Header.Get("Authorization") != "Bearer "+socktest.MachineKeyToken {
		t.Fatalf("ListPuppets calls %v", req)
	}
	if _, _, err := run(t, app, strings.NewReader(socktest.MachineKeyToken), "machine-key", "use"); err != nil {
		t.Fatalf("same key: %v", err)
	}
	// another key needs --force
	body := fmt.Sprintf("url = %q\nmachine_key = \"sock_mk_old_secret\"\n", srv.URL)
	if err := os.WriteFile(app.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, app, strings.NewReader(socktest.MachineKeyToken+"\n"), "machine-key", "use"); err == nil || !strings.Contains(err.Error(), "--force") || strings.Contains(err.Error(), "sock_mk_") {
		t.Fatalf("overwrite without --force: %v", err)
	}
	if _, _, err := run(t, app, strings.NewReader(socktest.MachineKeyToken+"\n"), "machine-key", "use", "--force"); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if f, _ := client.LoadFileConfig(app.ConfigPath); f.MachineKey != socktest.MachineKeyToken {
		t.Fatalf("file %+v", f)
	}
}

// security review 1: a key minted on another hub (SOCK_URL on create only) is
// refused, not saved beside this hub's url for sock-mcp to send there
func TestMachineKeyUseRefusesAKeyTheHubRejects(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	_, _, err := run(t, app, strings.NewReader("sock_mk_staging_secret\n"), "machine-key", "use")
	if err == nil || !strings.Contains(err.Error(), "does not accept") || strings.Contains(err.Error(), "staging_secret") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(app.ConfigPath); err == nil {
		t.Fatal("wrote the file")
	}
}

func TestMachineKeyUseRefusesWhatIsNotAMachineKey(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	for _, in := range []string{"", "sock_pt_dev_secret\n", "sock_mk_a b\n", "sock_mk_" + strings.Repeat("s", 5000)} {
		_, _, err := run(t, app, strings.NewReader(in), "machine-key", "use")
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("input of %d bytes: %v", len(in), err)
		}
	}
	if _, err := os.Stat(app.ConfigPath); err == nil || len(srv.CallsTo("ListPuppets")) != 0 {
		t.Fatalf("wrote the file (%v) or sent the input to the hub", err)
	}
}

// typed on a terminal the key would echo; it must be piped
func TestMachineKeyUseRefusesATerminal(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	app.StdinIsTTY = func() bool { return true }
	_, _, err := run(t, app, strings.NewReader(socktest.MachineKeyToken+"\n"), "machine-key", "use")
	if err == nil || !strings.Contains(err.Error(), "machine-key create") {
		t.Fatalf("err = %v", err)
	}
}

func TestMachineKeyUseRefusesAFileURLThatIsNotThisServer(t *testing.T) {
	srv := socktest.New(t)
	app := humanApp(t, srv, nil)
	if err := os.MkdirAll(filepath.Dir(app.ConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "url = \"https://other.example\"\n"
	if err := os.WriteFile(app.ConfigPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, app, strings.NewReader(socktest.MachineKeyToken), "machine-key", "use"); err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(app.ConfigPath); string(b) != body {
		t.Fatalf("file changed: %q", b)
	}
}
