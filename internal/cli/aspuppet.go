package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	client "github.com/guygrigsby/sock-client"
)

// The --as-puppet refusals are fixed text a hook can match; neither names a
// secret or a path.
const (
	errNoPuppetCredential = "send --as-puppet: no puppet credential; set SOCK_TOKEN, or run sock machine-key use or sock puppets use"
	errSessionAsPuppet    = "send --as-puppet: SOCK_TOKEN is a human session, not a puppet token or machine key"
	errSessionNeedsKey    = "send --as-puppet --session: a session's puppet is made with a machine key (SOCK_TOKEN or the config file's machine_key); run sock machine-key use"
)

var errNoPuppetCred = errors.New(errNoPuppetCredential)

// puppetClients speaks as the puppet sock-mcp would in project (the directory
// sock-mcp starts in; "" is the working directory): sock-client's
// LoadPuppetConfig and ProjectPuppet, the decision ResolvePuppet shares with
// sock-mcp. The human's token is never used. Lines from making the project's
// puppet go to stderr. A cached project puppet token is used without asking
// the hub first; for a machine key, recheck resolves again with
// that check, replacing the token of an erased puppet, and is for a call the
// hub answered TOKEN_INVALID. It is nil for any other credential.
func (a *App) puppetClients(ctx context.Context, project string, trustCache bool, stderr io.Writer) (c Clients, recheck func(context.Context) (Clients, error), err error) {
	cfg, o, err := a.puppetConfig(project, trustCache, stderr)
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

// puppetConfig is the credential --as-puppet speaks with, as sock-mcp loads
// it (LoadPuppetConfig), and the options it was loaded under.
func (a *App) puppetConfig(project string, trustCache bool, stderr io.Writer) (client.Config, client.PuppetOptions, error) {
	if a.flagToken != "" || a.flagURL != "" {
		return client.Config{}, client.PuppetOptions{}, errors.New("--as-puppet takes the server and credential as sock-mcp does, from SOCK_TOKEN or the config file; drop --token and --url")
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
		return client.Config{}, o, errNoPuppetCred
	}
	return cfg, o, err
}

// sessionPuppetClients speaks as the agent session's own puppet, as
// sock-mcp does for that session: sock-client's SessionPuppets,
// made with the machine key on the session's first send and named name. With
// setName the puppet is renamed when name changed; a failed rename goes to
// stderr and the send goes on.
func (a *App) sessionPuppetClients(ctx context.Context, s client.Session, name string, setName bool, stderr io.Writer) (Clients, error) {
	cfg, _, err := a.puppetConfig("", false, stderr)
	if err != nil && !errors.Is(err, errNoPuppetCred) {
		return Clients{}, err
	}
	if err != nil || client.TokenKind(cfg.Token) != client.KindMachineKey {
		return Clients{}, errors.New(errSessionNeedsKey)
	}
	ua := a.userAgent()
	sp := &client.SessionPuppets{
		API:    client.NewPuppetAPI(client.New(cfg, ua), a.Retrier),
		Dir:    client.ProjectsDir(a.Getenv, cfg),
		Host:   client.ShortHost(),
		Rename: client.NewRename(cfg, ua),
		Check:  client.NewTokenCheck(cfg, ua),
		Log:    func(f string, args ...any) { warnf(stderr, f, args...) },
	}
	tok, err := sp.Ensure(ctx, s, name)
	if err != nil {
		return Clients{}, err
	}
	if setName {
		if err := sp.SetName(ctx, s, name); err != nil {
			warnf(stderr, "%v", err)
		}
	}
	cfg.Token = tok
	a.token, a.url = cfg.Token, cfg.URL
	return client.New(cfg, ua), nil
}

// sessionName is the trimmed --session-name; set says the caller named the
// session. Without one it is the working directory's name and the first 8
// characters of the session id.
func sessionName(s client.Session, flag string) (name string, set bool) {
	if name = strings.TrimSpace(flag); name != "" {
		return name, true
	}
	dir := "session"
	if wd, err := os.Getwd(); err == nil {
		dir = filepath.Base(wd)
	}
	return fmt.Sprintf("%s %s", dir, s.ID[:min(8, len(s.ID))]), false
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
