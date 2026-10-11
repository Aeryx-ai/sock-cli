package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/guygrigsby/sock-cli/internal/config"
	"github.com/guygrigsby/sock-cli/internal/socktest"
	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

func TestNewSendsTokenAndUserAgent(t *testing.T) {
	srv := socktest.New(t)
	c := New(config.Config{URL: srv.URL, Token: socktest.Token}, "1.2.3")
	res, err := c.Identity.GetMe(context.Background(), connect.NewRequest(&sockv1.GetMeRequest{}))
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if res.Msg.GetAccount().GetId() != "puppet-1" {
		t.Fatalf("account %v", res.Msg.GetAccount())
	}
	_, _, uas, _ := srv.Snapshot()
	if len(uas) != 1 || uas[0] != "sock-cli/1.2.3" {
		t.Fatalf("user agents %q", uas)
	}
}

func TestBadTokenReason(t *testing.T) {
	srv := socktest.New(t)
	c := New(config.Config{URL: srv.URL, Token: "sock_pt_wrong"}, "dev")
	_, err := c.Identity.GetMe(context.Background(), connect.NewRequest(&sockv1.GetMeRequest{}))
	if Reason(err) != sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID {
		t.Fatalf("reason %v from %v", Reason(err), err)
	}
	if Transient(err) {
		t.Fatal("token rejection must not be transient")
	}
}

func TestTransient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unavailable", socktest.Unavailable(), true},
		{"deadline", connect.NewError(connect.CodeDeadlineExceeded, errors.New("slow")), true},
		{"rate limited", socktest.RateLimited("1"), true},
		{"other exhausted", connect.NewError(connect.CodeResourceExhausted, errors.New("x")), false},
		{"not member", socktest.NotMember(), false},
		{"plain", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Transient(c.err); got != c.want {
				t.Fatalf("Transient = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	d, ok := RetryAfter(socktest.RateLimited("7"), now)
	if !ok || d != 7*time.Second {
		t.Fatalf("seconds form: %v %v", d, ok)
	}
	d, ok = RetryAfter(socktest.RateLimited(now.Add(90*time.Second).Format(http.TimeFormat)), now)
	if !ok || d != 90*time.Second {
		t.Fatalf("date form: %v %v", d, ok)
	}
	d, ok = RetryAfter(socktest.RateLimited(now.Add(-time.Minute).Format(http.TimeFormat)), now)
	if !ok || d != 0 {
		t.Fatalf("past date: %v %v", d, ok)
	}
	if _, ok := RetryAfter(socktest.RateLimited("soon"), now); ok {
		t.Fatal("garbage header must be ignored")
	}
	if _, ok := RetryAfter(socktest.Unavailable(), now); ok {
		t.Fatal("no header means no Retry-After")
	}
}

func TestDescribeUsesReason(t *testing.T) {
	got := Describe(socktest.NotMember())
	if got == nil || got.Error() != "you are not a member of that conversation" {
		t.Fatalf("Describe = %v", got)
	}
}

func TestTokenNotSentOnRedirect(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
	}))
	t.Cleanup(other.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(first.Close)
	c := New(config.Config{URL: first.URL, Token: socktest.Token}, "dev")
	_, err := c.Identity.GetMe(context.Background(), connect.NewRequest(&sockv1.GetMeRequest{}))
	if err == nil {
		t.Fatal("redirect must surface as an error")
	}
	if leaked.Load() {
		t.Fatal("token reached the redirect target")
	}
}
