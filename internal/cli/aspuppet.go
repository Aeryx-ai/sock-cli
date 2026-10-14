package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	client "github.com/guygrigsby/sock-client"
)

// The --as-puppet refusals are fixed text a hook can match; neither names a
// secret or a path.
const (
	errNoPuppetCredential = "send --as-puppet: no puppet credential; set SOCK_TOKEN, or run sock machine-key use or sock puppets use"
	errSessionAsPuppet    = "send --as-puppet: SOCK_TOKEN is a human session, not a puppet token or machine key"
)

// puppetClients speaks as the puppet sock-mcp would in project (the directory
// sock-mcp starts in; "" is the working directory): sock-client's
// ResolvePuppet, the one decision both share. The human's token is never
// used. Lines from making the project's puppet go to stderr.
func (a *App) puppetClients(ctx context.Context, project string, stderr io.Writer) (Clients, error) {
	if a.flagToken != "" || a.flagURL != "" {
		return Clients{}, errors.New("--as-puppet takes the server and credential as sock-mcp does, from SOCK_TOKEN or the config file; drop --token and --url")
	}
	cfg, err := client.ResolvePuppet(ctx, client.PuppetOptions{
		Getenv:        a.Getenv,
		ConfigPath:    a.ConfigPath,
		WorkDir:       project,
		NoProjectHint: "set CLAUDE_PROJECT_DIR or pass --project the project's repository",
		UserAgent:     a.userAgent(),
		Log:           func(f string, args ...any) { _, _ = fmt.Fprintf(stderr, "sock: "+f+"\n", args...) },
	})
	if errors.Is(err, client.ErrNoPuppetCredential) {
		return Clients{}, errors.New(errNoPuppetCredential)
	}
	if err != nil {
		return Clients{}, err
	}
	if client.TokenKind(cfg.Token) != client.KindPuppet {
		return Clients{}, errors.New(errSessionAsPuppet)
	}
	a.token = cfg.Token
	return client.New(cfg, a.userAgent()), nil
}
