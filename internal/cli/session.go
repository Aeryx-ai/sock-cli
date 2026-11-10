package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"

	"github.com/aeryx-ai/sock-cli/internal/config"
	client "github.com/aeryx-ai/sock-client"
)

// The documents the app links in SockKit/Legal.swift, and the terms version
// this build ships.
const (
	termsURL     = "https://joinsock.com/terms/"
	privacyURL   = "https://joinsock.com/privacy/"
	termsVersion = "2026-10-11"
	inviteURL    = "https://joinsock.com/invite/"
)

// maxLabel is the device label limit, in characters.
const maxLabel = 64

// handleRE is the hub's handle rule, checked before anything is sent.
var handleRE = regexp.MustCompile(`^[a-z0-9_]{3,20}$`)

// signInFlags are login's flags: the device, the browser, and what a new
// person's registration needs when there is no terminal to ask on.
type signInFlags struct {
	label     string
	noBrowser bool
	handle    string
	name      string
	terms     string
	invite    string
}

func (f *signInFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.label, "label", "", "device label (default \"sock CLI on <hostname>\")")
	cmd.Flags().BoolVar(&f.noBrowser, "no-browser", false, "do not try to open a browser; the page and the code are printed either way")
	cmd.Flags().StringVar(&f.handle, "handle", "", "when registering: handle, lowercase a-z, 0-9 and _, 3 to 20 characters (prompted for on a terminal)")
	cmd.Flags().StringVar(&f.name, "name", "", "when registering: display name (prompted for on a terminal)")
	cmd.Flags().StringVar(&f.terms, "accept-terms", "", "when registering: the terms version you accept, "+termsVersion+" in this build (asked on a terminal)")
	cmd.Flags().StringVar(&f.invite, "invite", "", "when registering while registration is invite-only: invite code (prompted for on a terminal)")
}

func newLoginCmd(app *App) *cobra.Command {
	var f signInFlags
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in with a code you confirm in a browser on any device, and save a session token",
		Long: "Sign in as a human. sock shows a short code and the hub page that takes it, opens your browser to that page when it can, " +
			"and waits for you to sign in there with Apple, Google or email; the browser can be on any device. " +
			"The session token goes to the config file, never to the terminal.\n\n" +
			"A sign-in to an address with no sock account registers one: on a terminal you are asked for a handle, a name and the terms " +
			"(" + termsURL + ", " + privacyURL + "; this build ships terms version " + termsVersion + "), and for an invite while registration is invite-only; " +
			"off a terminal pass --handle, --name and --accept-terms, and --invite when required.\n\n" +
			"Run it again any time: a session the server still accepts is kept (\"already signed in\") and no browser opens; " +
			"one it rejects is removed and replaced. login also keeps a machine key for this host (machine_key) so sock-mcp makes " +
			"each project its own puppet: one that works is kept, a missing or rejected one is minted, one that cannot be checked is kept. " +
			"A puppet token in the config file's token is refused: it is shown only once.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return app.login(cmd, f) },
	}
	f.register(cmd)
	return cmd
}

// newRegisterCmd is the removed command, kept only to say where it went.
func newRegisterCmd(*App) *cobra.Command {
	return &cobra.Command{
		Use:                "register",
		Short:              "Removed: sock login creates an account when you have none",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("sock register is gone: sock login creates an account when you have none")
		},
	}
}

// login repairs this machine's sign-in: a session the hub accepts is kept,
// one it rejects is removed and replaced by the terminal sign-in (sock-contract
// ADR 52), which registers a new person with the ticket it may answer. Then
// the machine key gets the same check.
func (a *App) login(cmd *cobra.Command, f signInFlags) error {
	cfg, err := a.load(true)
	if err != nil {
		return err
	}
	if a.ConfigPath == "" {
		return errors.New("no config file path: set HOME or XDG_CONFIG_HOME")
	}
	existing, err := config.FileToken(a.ConfigPath)
	if err != nil {
		return err
	}
	if existing != "" && !strings.HasPrefix(existing, humanTokenPrefix) {
		return a.tokenExists() // a puppet token there is shown only once
	}
	// the file's credentials go only to the file's server
	if why, err := config.CredentialConflict(a.ConfigPath, cfg.URL, cfg.AccessGate); err != nil {
		return err
	} else if why != "" {
		return errors.New(why)
	}
	label, err := a.deviceLabel(f.label)
	if err != nil {
		return err
	}
	if done, err := a.keepSession(cmd, cfg, existing); done || err != nil {
		return err
	}
	stderr := cmd.ErrOrStderr()
	if a.Getenv("SOCK_TOKEN") != "" {
		warn(stderr, "SOCK_TOKEN is set and overrides the token sock login saves; unset it to use the new session")
	}
	cfg.Token = "" // sign-in is anonymous: never send another token along
	c := newClients(cfg, a.userAgent())
	ctx := cmd.Context()
	start, err := unary(ctx, a, c.Identity.StartTerminalSignIn, &sockv1.StartTerminalSignInRequest{DeviceLabel: label})
	if err != nil {
		return err
	}
	if err := a.showCode(stderr, f.noBrowser, cfg.URL, start); err != nil {
		return err
	}
	res, err := a.waitForBrowser(ctx, c, start)
	if err != nil {
		return err
	}
	warn(stderr, "sign-in received")
	session := res.GetSession()
	if r := res.GetRegistration(); r != nil {
		if session, err = a.registerNew(cmd, c, f, label, r.GetRegistrationTicket()); err != nil {
			return err
		}
	}
	if !strings.HasPrefix(session.GetToken(), humanTokenPrefix) {
		return errors.New("the server returned no session token")
	}
	if err := config.SetNewToken(a.ConfigPath, session.GetToken(), cfg.URL, cfg.AccessGate); errors.Is(err, config.ErrTokenExists) {
		// A token arrived in the file during the browser wait: keep it.
		return fmt.Errorf("%w; the new session (device %s) was not saved, revoke it with sock devices revoke", a.tokenExists(), session.GetDevice().GetId())
	} else if err != nil {
		return fmt.Errorf("signed in, but saving the token failed (%w); revoke device %s with sock devices revoke from another session", err, session.GetDevice().GetId())
	}
	a.keepMachineKey(cmd, cfg, session.GetToken())
	session.Token = ""
	if a.jsonOut {
		return a.printer().json(cmd.OutOrStdout(), session)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "signed in as @%s on device %s\n", line(session.GetAccount().GetHuman().GetHandle()), line(session.GetDevice().GetId()))
	return err
}

// waitForBrowser polls FinishTerminalSignIn at the interval the hub names
// until a browser has finished, the sign-in expires or LoginTimeout passes.
// The first poll waits a full interval: Start counted as one.
func (a *App) waitForBrowser(ctx context.Context, c Clients, start *sockv1.StartTerminalSignInResponse) (*sockv1.FinishTerminalSignInResponse, error) {
	timeout := a.LoginTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	if exp := start.GetExpiresAt(); exp != nil && exp.AsTime().Before(deadline) {
		deadline = exp.AsTime()
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	wait := start.GetPollInterval().AsDuration()
	if wait <= 0 {
		wait = 5 * time.Second
	}
	timedOut := errors.New("sign-in timed out; run sock login again")
	for {
		if err := a.Sleep(ctx, wait); err != nil {
			return nil, timedOut
		}
		res, err := unary(ctx, a, c.Identity.FinishTerminalSignIn, &sockv1.FinishTerminalSignInRequest{TerminalSignIn: start.GetTerminalSignIn()})
		switch r := client.Reason(err); {
		case ctx.Err() != nil:
			return nil, timedOut
		case r == sockv1.ErrorReason_ERROR_REASON_RATE_LIMITED:
			if after, ok := client.RetryAfter(err, time.Now()); ok && after > 0 {
				wait = after
			}
			continue
		case err != nil:
			return nil, err
		}
		if p := res.GetPending(); p != nil {
			if d := p.GetRetryAfter().AsDuration(); d > 0 {
				wait = d
			}
			continue
		}
		return res, nil
	}
}

// registerNew is the new person's registration with the ticket the sign-in
// answered: a handle, a name, the terms and, while registration is
// invite-only, an invite, each from its flag or asked for on a terminal.
func (a *App) registerNew(cmd *cobra.Command, c Clients, f signInFlags, label, ticket string) (*sockv1.Session, error) {
	stderr := cmd.ErrOrStderr()
	warn(stderr, "No sock account for this address yet. Let's make one.")
	ctx := cmd.Context()
	policy, err := unary(ctx, a, c.Identity.GetRegistrationPolicy, &sockv1.GetRegistrationPolicyRequest{})
	if err != nil {
		return nil, err
	}
	tty := a.StdinIsTTY()
	// one reader for every prompt: a fresh bufio.Reader per question would
	// drop the lines already buffered
	in := bufio.NewReader(cmd.InOrStdin())
	ask := func(prompt string) (string, error) {
		if _, err := fmt.Fprint(stderr, line(prompt)); err != nil {
			return "", err
		}
		answer, err := in.ReadString('\n')
		if err != nil && answer == "" {
			return "", fmt.Errorf("read answer: %w", err)
		}
		return strings.TrimSpace(answer), nil
	}
	missing := func(what string) error {
		need := "--handle, --name and --accept-terms"
		if policy.GetInviteRequired() {
			need += " (and --invite)"
		}
		return fmt.Errorf("registration needs %s: run sock login on a terminal, or pass %s", what, need)
	}
	handle := strings.TrimPrefix(f.handle, "@")
	for handle == "" || !handleRE.MatchString(handle) {
		if handle != "" {
			warn(stderr, "a handle is 3 to 20 of lowercase a-z, 0-9 and _")
		}
		if !tty {
			return nil, missing("a handle")
		}
		if handle, err = ask("Handle (3 to 20 of a-z, 0-9, _): @"); err != nil {
			return nil, err
		}
		handle = strings.TrimPrefix(handle, "@")
	}
	name := strings.TrimSpace(f.name)
	for name == "" || utf8.RuneCountInString(name) > maxLabel {
		if name != "" {
			warnf(stderr, "a display name is 1 to %d characters", maxLabel)
		}
		if !tty {
			return nil, missing("a display name")
		}
		if name, err = ask("Display name: "); err != nil {
			return nil, err
		}
		name = strings.TrimSpace(name)
	}
	invite := f.invite
	if policy.GetInviteRequired() && invite == "" {
		if !tty {
			return nil, missing("an invite")
		}
		warn(stderr, "Registration needs an invite; request one at "+inviteURL)
		if invite, err = ask("Invite code: "); err != nil {
			return nil, err
		}
	}
	terms := f.terms
	if terms == "" {
		if !tty {
			return nil, missing("the terms accepted")
		}
		warnf(stderr, "Terms of service: %s\nPrivacy policy: %s\nThis build ships terms version %s.", termsURL, privacyURL, termsVersion)
		yes, err := ask("Type yes to accept them: ")
		if err != nil {
			return nil, err
		}
		if yes != "yes" {
			return nil, errors.New("not registered")
		}
		terms = termsVersion
	}
	for attempt := range 2 {
		res, err := unary(ctx, a, c.Identity.RegisterWithEmail, &sockv1.RegisterWithEmailRequest{
			RegistrationTicket: ticket, DeviceLabel: label, InviteCode: invite, Handle: handle, DisplayName: name, AcceptedTermsVersion: terms,
		})
		r := client.Reason(err)
		switch {
		case err == nil:
			return res.GetSession(), nil
		case r == sockv1.ErrorReason_ERROR_REASON_TERMS_NOT_ACCEPTED:
			return nil, &rewritten{fmt.Sprintf("terms version %s is not the server's current one; update sock, then read the terms again at %s", terms, termsURL), err}
		case r == sockv1.ErrorReason_ERROR_REASON_REGISTRATION_TICKET_INVALID:
			return nil, &rewritten{"the sign-in expired before registration finished; run sock login again", err}
		case attempt == 0 && tty && r == sockv1.ErrorReason_ERROR_REASON_HANDLE_TAKEN:
			warnf(stderr, "@%s is taken", handle)
			if handle, err = ask("Another handle: @"); err != nil {
				return nil, err
			}
			handle = strings.TrimPrefix(handle, "@")
		case attempt == 0 && tty && r == sockv1.ErrorReason_ERROR_REASON_INVITE_INVALID:
			warn(stderr, "that invite is not valid")
			if invite, err = ask("Invite code: "); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
	}
	return nil, errors.New("not registered")
}

// showCode tells the person where to sign in: the page, the code, and
// whether a browser was opened to the page with the code filled in. The
// page and the code are always printed, since the browser may be on
// another device.
func (a *App) showCode(stderr io.Writer, noBrowser bool, hub string, start *sockv1.StartTerminalSignInResponse) error {
	if _, err := fmt.Fprintf(stderr, "To sign in, open this page on any device and enter the code:\n\n  %s\n\n  %s\n\n", line(start.GetVerificationUrl()), line(start.GetUserCode())); err != nil {
		return err
	}
	how := "Open it on any device; waiting for you to finish."
	if !noBrowser {
		if err := openOnHub(a.OpenBrowser, hub, start.GetVerificationUrlComplete()); err == nil {
			how = "Opened your browser to that page with the code filled in; waiting for you to finish."
		} else {
			warnf(stderr, "could not open a browser: %v", err)
		}
	}
	_, err := fmt.Fprintln(stderr, how)
	return err
}

// keepSession checks existing, the config file's session, with the hub. done
// is true when it is valid: it is kept and reported, and the machine key
// checked. A rejected session is removed so a sign-in can replace it; one that
// cannot be checked is refused with the file untouched.
func (a *App) keepSession(cmd *cobra.Command, cfg config.Config, existing string) (done bool, err error) {
	if existing == "" {
		return false, nil
	}
	a.token = existing
	me, err := a.checkSession(cmd.Context(), cfg, existing)
	switch {
	case rejected(err):
		if err := config.RemoveCheckedToken(a.ConfigPath, existing); errors.Is(err, config.ErrTokenChanged) {
			return false, errors.New("the config file's token changed while sock login checked it; run sock login again")
		} else if err != nil {
			return false, err
		}
		return false, nil
	case err != nil:
		return false, fmt.Errorf("could not check the session in %s (%w); it is unchanged, run sock login again", a.ConfigPath, err)
	}
	a.keepMachineKey(cmd, cfg, existing)
	if a.jsonOut {
		return true, a.printer().json(cmd.OutOrStdout(), me)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "already signed in as @%s\n", line(me.GetAccount().GetHuman().GetHandle()))
	return true, err
}

// checkSession is the session check: GetMe with token at cfg's server. A
// server, token or gate that fails client.Validate is sent nothing.
func (a *App) checkSession(ctx context.Context, cfg config.Config, token string) (*sockv1.GetMeResponse, error) {
	cfg.Token = token
	if err := client.Validate(cfg.Config); err != nil {
		return nil, err
	}
	return unary(ctx, a, newClients(cfg, a.userAgent()).Identity.GetMe, &sockv1.GetMeRequest{})
}

// checkKey is the machine key check: ListPuppets with key at cfg's server,
// the one call a machine key may make that changes nothing. Validated like
// checkSession.
func (a *App) checkKey(ctx context.Context, cfg config.Config, key string) error {
	cfg.Token = key
	if err := client.Validate(cfg.Config); err != nil {
		return err
	}
	_, err := unary(ctx, a, newClients(cfg, a.userAgent()).Identity.ListPuppets, &sockv1.ListPuppetsRequest{})
	return err
}

// rejected reports whether the hub answered that it no longer accepts the
// credential a call carried: TOKEN_INVALID, which the hub attaches to every
// unknown, revoked or wrong token. A bare 401 (a proxy in front of the hub)
// is not that answer.
func rejected(err error) bool {
	return client.Reason(err) == sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID
}

// keepMachineKey makes sure the config file holds a machine key the hub
// accepts, so sock-mcp makes each project its own puppet: one that works is
// kept, a missing or rejected one is minted with session and saved (a rejected
// one is overwritten, not revoked: the hub no longer accepts it), and one that
// cannot be checked is kept with a warning. The login stands either way: a key
// that cannot be minted or saved only says how to finish.
func (a *App) keepMachineKey(cmd *cobra.Command, cfg config.Config, session string) {
	stderr := cmd.ErrOrStderr()
	ctx := cmd.Context()
	finish := "sign-in is saved; to let sock-mcp make a puppet per project, run: sock machine-key create | sock machine-key use"
	dead := "" // the key found rejected, replaced only while the file holds it
	if fc, err := client.LoadFileConfig(a.ConfigPath); err == nil && strings.TrimSpace(fc.MachineKey) != "" {
		held := strings.TrimSpace(fc.MachineKey)
		switch err := a.checkKey(ctx, cfg, held); {
		case err == nil:
			warn(stderr, "machine key works")
			return
		case !rejected(err):
			warnf(stderr, "could not check the machine key (%v); kept it", err)
			return
		}
		dead = held
	}
	cfg.Token = session
	short, _, _ := strings.Cut(a.host(), ".")
	label, err := labelOr("", short)
	if err != nil {
		warnf(stderr, "no machine key kept: %v; %s", err, finish)
		return
	}
	res, err := unary(ctx, a, newClients(cfg, a.userAgent()).Identity.MintMachineKey, &sockv1.MintMachineKeyRequest{Label: label})
	if err != nil {
		warnf(stderr, "no machine key kept (%v); %s", client.Describe(err), finish)
		return
	}
	// dead "" fills an empty machine_key only
	if err := config.ReplaceMachineKey(a.ConfigPath, dead, res.GetToken(), cfg.URL, cfg.AccessGate); errors.Is(err, config.ErrMachineKeyExists) {
		warnf(stderr, "a newer machine key is in the config file, so it was kept; revoke the one just minted with sock devices revoke %s", res.GetDevice().GetId())
		return
	} else if err != nil {
		warnf(stderr, "no machine key kept (%v); %s", err, finish)
		return
	}
	if dead != "" {
		warn(stderr, "replaced a machine key the server no longer accepts")
		return
	}
	warnf(stderr, "kept a machine key for %s: each project you open gets its own puppet", label)
}

// tokenExists is the refusal to overwrite the config file's token: a puppet
// token there is shown only once.
func (a *App) tokenExists() error {
	return fmt.Errorf("%s already holds a token; run sock logout first, or keep a copy of a puppet token (it is shown only once) and remove it, or set XDG_CONFIG_HOME to use another config", a.ConfigPath)
}

// deviceLabel is the flag's label, else "sock CLI on <hostname>" cut to
// maxLabel characters.
func (a *App) deviceLabel(flag string) (string, error) {
	return labelOr(flag, "sock CLI on "+a.host())
}

// labelOr is the flag's label, refused past maxLabel characters, else dflt
// cut to maxLabel characters.
func labelOr(flag, dflt string) (string, error) {
	if flag != "" {
		if utf8.RuneCountInString(flag) > maxLabel {
			return "", fmt.Errorf("--label is longer than %d characters", maxLabel)
		}
		return flag, nil
	}
	label := []rune(dflt)
	return string(label[:min(len(label), maxLabel)]), nil
}

// host is this machine's hostname, or "unknown host".
func (a *App) host() string {
	host, err := a.Hostname()
	if err != nil || host == "" {
		return "unknown host"
	}
	return host
}

// warn writes a diagnostic line. A failing stderr has no recovery path.
// msg can carry server or file text, so it is printed through line.
func warn(w io.Writer, msg string) { _, _ = fmt.Fprintln(w, "sock: "+line(msg)) }

// warnf is warn with a format.
func warnf(w io.Writer, format string, args ...any) { warn(w, fmt.Sprintf(format, args...)) }

// Report prints the error a command ended with, through term: it can carry
// server text.
func Report(w io.Writer, err error) { _, _ = fmt.Fprintln(w, "sock: "+term(err.Error(), true)) }

func newLogoutCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Sign out the session in the config file and remove its token",
		Long: "Sign out the config file's session at the file's server and remove its token. The machine_key stays, so " +
			"sessions on this machine can still message you; sock settings --message-owner=off stops that.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token, err := config.FileToken(app.ConfigPath)
			if err != nil {
				return err
			}
			switch {
			case token == "":
				return fmt.Errorf("no token in %s; nothing to sign out", app.ConfigPath)
			case strings.HasPrefix(token, puppetTokenPrefix):
				return errors.New("the config file holds a puppet token; puppets do not sign out, their owner revokes them with sock devices revoke")
			}
			// the file's session at the file's server, whatever SOCK_TOKEN holds
			cfg, err := config.Load(config.Sources{FlagURL: app.flagURL, Getenv: app.Getenv, Path: app.ConfigPath, FileTokenOnly: true})
			if err != nil {
				return err
			}
			app.token = cfg.Token
			c := newClients(cfg, app.userAgent())
			_, err = call(cmd.Context(), app, func(ctx context.Context) (*sockv1.SignOutResponse, error) {
				res, err := c.Identity.SignOut(ctx, connect.NewRequest(&sockv1.SignOutRequest{}))
				if err != nil {
					return nil, err
				}
				return res.Msg, nil
			})
			// TOKEN_INVALID: the session is already gone, so the token is dead weight.
			if err != nil && client.Reason(err) != sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID {
				return err
			}
			if err := config.RemoveToken(app.ConfigPath); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), "signed out"); err != nil {
				return err
			}
			if fc, err := client.LoadFileConfig(app.ConfigPath); err == nil && strings.TrimSpace(fc.MachineKey) != "" {
				warn(cmd.ErrOrStderr(), "the machine key stays: sessions here can still message you; sock settings --message-owner=off stops that")
			}
			return nil
		},
	}
}

// openOnHub opens page in the browser only when it is on the hub the
// person configured, over https or the hub's own scheme: the hub chose the
// page, but a URL it did not serve itself is not handed to the opener.
func openOnHub(open func(string) error, hub, page string) error {
	h, err := url.Parse(hub)
	if err != nil {
		return err
	}
	p, err := url.Parse(page)
	if err != nil {
		return err
	}
	if p.Scheme != h.Scheme || p.Host != h.Host || p.Scheme != "https" && p.Scheme != "http" {
		return fmt.Errorf("the hub named %s, which is not on %s", line(p.Host), line(h.Host))
	}
	return open(page)
}
