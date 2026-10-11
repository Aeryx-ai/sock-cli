// Package client wraps the generated Connect clients with auth, a User-Agent,
// error classification and retries.
package client

import (
	"net/http"

	"github.com/guygrigsby/sock-cli/internal/config"
	"github.com/guygrigsby/sock-contract/gen/go/sock/v1/sockv1connect"
)

// Clients are the two sock services.
type Clients struct {
	Identity  sockv1connect.IdentityServiceClient
	Messaging sockv1connect.MessagingServiceClient
}

// New builds clients that send cfg.Token and a sock-cli User-Agent on every
// request. No client-wide timeout: Subscribe streams indefinitely. Unary
// callers set a per-call deadline.
func New(cfg config.Config, version string) Clients {
	hc := &http.Client{Transport: &transport{
		base:  http.DefaultTransport,
		token: cfg.Token,
		ua:    "sock-cli/" + version,
	}}
	return Clients{
		Identity:  sockv1connect.NewIdentityServiceClient(hc, cfg.URL),
		Messaging: sockv1connect.NewMessagingServiceClient(hc, cfg.URL),
	}
}

type transport struct {
	base  http.RoundTripper
	token string
	ua    string
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	r.Header.Set("User-Agent", t.ua)
	return t.base.RoundTrip(r)
}
