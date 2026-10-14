package cli

import (
	"github.com/guygrigsby/sock-cli/internal/config"
	client "github.com/guygrigsby/sock-client"
)

// Clients are the sock services a human or puppet calls.
type Clients = client.Clients

// newClients builds sock-client's clients for cfg: the token (when there is
// one) and the access gate (when set) go to the configured host only, and no
// redirect is followed.
func newClients(cfg config.Config, userAgent string) Clients {
	return client.New(cfg.Config, userAgent)
}
