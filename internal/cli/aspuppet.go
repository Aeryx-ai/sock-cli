package cli

import (
	"context"
	"errors"
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
// LoadPuppetConfig and ProjectPuppet, the decision ResolvePuppet shares with
// sock-mcp. The human's token is never used. Lines from making the project's
// puppet go to stderr. A cached project puppet token is used without asking
// the hub first (sock-cli-7ut); for a machine key, recheck resolves again with
// that check, replacing the token of an erased puppet, and is for a call the
// hub answered TOKEN_INVALID. It is nil for any other credential.
func (a *App) puppetClients(ctx context.Context, project string, trustCache bool, stderr io.Writer) (c Clients, recheck func(context.Context) (Clients, error), err error) {
	if a.flagToken != "" || a.flagURL != "" {
		return Clients{}, nil, errors.New("--as-puppet takes the server and credential as sock-mcp does, from SOCK_TOKEN or the config file; drop --token and --url")
	}
	o := client.PuppetOptions{
		Getenv:        a.Getenv,
		ConfigPath:    a.ConfigPath,
		WorkDir:       project,
		NoProjectHint: "set CLAUDE_PROJECT_DIR or pass --project the project's repository",
		UserAgent:     a.userAgent(),
		Log:           func(f string, args ...any) { warnf(stderr, f, args...) },
		TrustCache:    trustCache,
	}
	cfg, err := client.LoadPuppetConfig(o)
	if errors.Is(err, client.ErrNoPuppetCredential) {
		return Clients{}, nil, errors.New(errNoPuppetCredential)
	}
	if err != nil {
		return Clients{}, nil, err
	}
	if client.TokenKind(cfg.Token) == client.KindMachineKey {
		recheck = func(ctx context.Context) (Clients, error) {
			o.TrustCache = false
			return a.projectPuppetClients(ctx, cfg, o)
		}
	}
	c, err = a.projectPuppetClients(ctx, cfg, o)
	return c, recheck, err
}

// projectPuppetClients is ProjectPuppet's puppet for cfg, as the caller.
func (a *App) projectPuppetClients(ctx context.Context, cfg client.Config, o client.PuppetOptions) (Clients, error) {
	cfg, err := client.ProjectPuppet(ctx, cfg, o)
	if err != nil {
		return Clients{}, err
	}
	if client.TokenKind(cfg.Token) != client.KindPuppet {
		return Clients{}, errors.New(errSessionAsPuppet)
	}
	a.token, a.url = cfg.Token, cfg.URL
	return client.New(cfg, a.userAgent()), nil
}
