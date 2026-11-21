package cli

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"

	"github.com/aeryx-ai/sock-cli/internal/socktest"
)

func TestLimits(t *testing.T) {
	app, srv := signedIn(t)
	srv.Limits = &sockv1.PlanLimits{Puppets: 10, GroupMembers: 20, PuppetSendsPerMinute: 60}
	srv.LimitRequests = []*sockv1.LimitRequest{
		{Id: "lr-2", Key: sockv1.LimitKey_LIMIT_KEY_PUPPETS, Requested: 50, State: sockv1.LimitRequestState_LIMIT_REQUEST_STATE_PENDING},
		{Id: "lr-1", Key: sockv1.LimitKey_LIMIT_KEY_GROUP_MEMBERS, Requested: 40, State: sockv1.LimitRequestState_LIMIT_REQUEST_STATE_APPROVED, Granted: 30},
		{Id: "lr-0", Key: sockv1.LimitKey_LIMIT_KEY_PUPPET_SENDS_PER_MINUTE, Requested: 600, State: sockv1.LimitRequestState_LIMIT_REQUEST_STATE_DECLINED},
	}
	out, _ := mustRun(t, app, "limits")
	want := "puppets 10\ngroup-members 20\npuppet-sends 60\n" +
		"puppets 50 pending\ngroup-members 40 approved granted 30\npuppet-sends 600 declined\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}

	out, _ = mustRun(t, app, "--json", "limits")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("json lines %q", out)
	}
	var l sockv1.PlanLimits
	if err := protojson.Unmarshal([]byte(lines[0]), &l); err != nil || l.GetGroupMembers() != 20 {
		t.Fatalf("limits line %q: %v", lines[0], err)
	}
	var r sockv1.LimitRequest
	if err := protojson.Unmarshal([]byte(lines[2]), &r); err != nil || r.GetGranted() != 30 {
		t.Fatalf("request line %q: %v", lines[2], err)
	}
}

func TestLimitsPuppetTokenSkipsRequests(t *testing.T) {
	srv := socktest.New(t)
	srv.Limits = &sockv1.PlanLimits{Puppets: 10, GroupMembers: 20, PuppetSendsPerMinute: 60}
	out, _, err := run(t, testApp(t, srv), nil, "limits")
	if err != nil || out != "puppets 10\ngroup-members 20\npuppet-sends 60\n" {
		t.Fatalf("got %q, %v", out, err)
	}
	if n := len(srv.CallsTo("ListMyLimitRequests")); n != 0 {
		t.Fatalf("ListMyLimitRequests called %d times", n)
	}
}

func TestLimitsRequest(t *testing.T) {
	keys := map[string]sockv1.LimitKey{
		"puppets":       sockv1.LimitKey_LIMIT_KEY_PUPPETS,
		"group-members": sockv1.LimitKey_LIMIT_KEY_GROUP_MEMBERS,
		"puppet-sends":  sockv1.LimitKey_LIMIT_KEY_PUPPET_SENDS_PER_MINUTE,
	}
	for name, key := range keys {
		app, srv := signedIn(t)
		out, _ := mustRun(t, app, "limits", "request", name, "50", "--reason", "more agents")
		if want := "requested " + name + " 50, pending\n"; out != want {
			t.Fatalf("got %q, want %q", out, want)
		}
		req := lastRequest[*sockv1.RequestLimitIncreaseRequest](t, srv, "RequestLimitIncrease")
		if req.GetKey() != key || req.GetRequested() != 50 || req.GetReason() != "more agents" {
			t.Fatalf("request %v", req)
		}
	}

	app, _ := signedIn(t)
	out, _ := mustRun(t, app, "--json", "limits", "request", "puppets", "50", "--reason", "more agents")
	var r sockv1.LimitRequest
	if err := protojson.Unmarshal([]byte(out), &r); err != nil || r.GetId() != "lr-new" || r.GetState() != sockv1.LimitRequestState_LIMIT_REQUEST_STATE_PENDING {
		t.Fatalf("json %q: %v", out, err)
	}
}

func TestLimitsRequestRefusesBadInput(t *testing.T) {
	cases := map[string][]string{
		"unknown key":   {"limits", "request", "sections", "5", "--reason", "x"},
		"zero":          {"limits", "request", "puppets", "0", "--reason", "x"},
		"not a number":  {"limits", "request", "puppets", "lots", "--reason", "x"},
		"over int32":    {"limits", "request", "puppets", "3000000000", "--reason", "x"},
		"no reason":     {"limits", "request", "puppets", "50"},
		"empty reason":  {"limits", "request", "puppets", "50", "--reason", ""},
		"long reason":   {"limits", "request", "puppets", "50", "--reason", strings.Repeat("é", 501)},
		"missing value": {"limits", "request", "puppets", "--reason", "x"},
	}
	for name, args := range cases {
		app, srv := signedIn(t)
		if _, _, err := run(t, app, nil, args...); err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if n := len(srv.CallsTo("RequestLimitIncrease")); n != 0 {
			t.Fatalf("%s: sent %d requests", name, n)
		}
	}
	app, _ := signedIn(t)
	if _, _, err := run(t, app, nil, "limits", "request", "puppets", "50", "--reason", strings.Repeat("é", 500)); err != nil {
		t.Fatalf("500 character reason refused: %v", err)
	}
}
