package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/guygrigsby/sock-cli/internal/config"
	client "github.com/guygrigsby/sock-client"
)

// headersAt sends one request to target through newClients' transport for a
// config pointing at configured, and returns what target received.
func headersAt(t *testing.T, cfg config.Config, target string) http.Header {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	t.Cleanup(srv.Close)
	if cfg.URL == "" {
		cfg.URL = srv.URL
	}
	if target == "" {
		target = srv.URL
	}
	tr := newClientsTransport(t, cfg)
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return got
}

// newClientsTransport is the transport newClients builds for cfg.
func newClientsTransport(t *testing.T, cfg config.Config) *transport {
	t.Helper()
	u, err := url.Parse(cfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &transport{base: http.DefaultTransport, token: cfg.Token, gate: cfg.AccessGate, ua: "sock-cli/test", scheme: u.Scheme, host: u.Host}
}

func TestTransportSendsTokenAndGateToConfiguredHost(t *testing.T) {
	h := headersAt(t, config.Config{Config: client.Config{Token: "sock_st_d_s"}, AccessGate: "gate-1"}, "")
	if h.Get("Authorization") != "Bearer sock_st_d_s" || h.Get(accessGateHeader) != "gate-1" || h.Get("User-Agent") != "sock-cli/test" {
		t.Fatalf("headers %v", h)
	}
}

func TestTransportKeepsTokenAndGateFromOtherHosts(t *testing.T) {
	cfg := config.Config{Config: client.Config{URL: "https://api.example", Token: "sock_st_d_s"}, AccessGate: "gate-1"}
	h := headersAt(t, cfg, "")
	if h.Get("Authorization") != "" || h.Get(accessGateHeader) != "" {
		t.Fatalf("secrets sent to another host: %v", h)
	}
}

func TestTransportOmitsEmptyTokenAndGate(t *testing.T) {
	h := headersAt(t, config.Config{}, "")
	if _, ok := h["Authorization"]; ok {
		t.Fatalf("Authorization sent without a token: %v", h)
	}
	if _, ok := h[accessGateHeader]; ok {
		t.Fatalf("gate sent without one configured: %v", h)
	}
}
