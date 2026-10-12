package cli

import (
	"net/http"
	"net/url"

	"github.com/guygrigsby/sock-cli/internal/config"
	"github.com/guygrigsby/sock-contract/gen/go/sock/v1/sockv1connect"
)

// accessGateHeader carries the staging access gate.
const accessGateHeader = "Sock-Access-Gate"

// Clients are the three sock services a human or puppet calls.
type Clients struct {
	Identity   sockv1connect.IdentityServiceClient
	Messaging  sockv1connect.MessagingServiceClient
	Moderation sockv1connect.ModerationServiceClient
}

// newClients builds clients that send the token (when there is one) and the
// access gate (when set) to the configured scheme and host only, and the
// user agent everywhere. No client-wide timeout: Subscribe streams
// indefinitely. Redirects are never followed.
func newClients(cfg config.Config, userAgent string) Clients {
	base, _ := url.Parse(cfg.URL) // Load validated the URL; a nil-safe zero URL matches nothing
	if base == nil {
		base = &url.URL{}
	}
	hc := &http.Client{
		Transport: &transport{
			base:   http.DefaultTransport,
			token:  cfg.Token,
			gate:   cfg.AccessGate,
			ua:     userAgent,
			scheme: base.Scheme,
			host:   base.Host,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return Clients{
		Identity:   sockv1connect.NewIdentityServiceClient(hc, cfg.URL),
		Messaging:  sockv1connect.NewMessagingServiceClient(hc, cfg.URL),
		Moderation: sockv1connect.NewModerationServiceClient(hc, cfg.URL),
	}
}

type transport struct {
	base         http.RoundTripper
	token, gate  string
	ua           string
	scheme, host string
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if r.URL.Scheme == t.scheme && r.URL.Host == t.host {
		if t.token != "" {
			r.Header.Set("Authorization", "Bearer "+t.token)
		}
		if t.gate != "" {
			r.Header.Set(accessGateHeader, t.gate)
		}
	}
	r.Header.Set("User-Agent", t.ua)
	return t.base.RoundTrip(r)
}
