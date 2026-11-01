package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"

	"github.com/aeryx-ai/sock-cli/internal/config"
	client "github.com/aeryx-ai/sock-client"
	"github.com/aeryx-ai/sock-client/login"
)

// The documents the app links in SockKit/Legal.swift, and the terms version
// this build ships.
const (
	termsURL     = "https://joinsock.com/terms/"
	privacyURL   = "https://joinsock.com/privacy/"
	termsVersion = "2026-10-11"
)

// maxLabel is the device label limit, in characters.
const maxLabel = 64

// signInFlags are the flags login and register share.
type signInFlags struct {
	label     string
	noBrowser bool
	port      int
}

func (f *signInFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.label, "label", "", "device label (default \"sock CLI on <hostname>\")")
	cmd.Flags().BoolVar(&f.noBrowser, "no-browser", false, "print the sign-in URL instead of opening a browser")
	cmd.Flags().IntVar(&f.port, "port", 0, "loopback listener port, 1024 to 65535 (default any free port)")
}

// signInRPC calls SignInWithApple or RegisterWithApple with what the browser
// handoff produced.
type signInRPC func(ctx context.Context, c Clients, r login.Result, label string) (*sockv1.Session, error)

func newLoginCmd(app *App) *cobra.Command {
	var f signInFlags
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in with Apple in the browser and save a session token",
		Long: "Sign in as a human with Sign in with Apple. sock opens the hub's sign-in page in your browser and listens on 127.0.0.1 for the result, " +
			"then writes the session token to the config file. On a remote machine, pass --no-browser and --port, forward the port (ssh -L <port>:127.0.0.1:<port>) and open the URL on your own machine.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return app.signIn(cmd, f, func(ctx context.Context, c Clients, r login.Result, label string) (*sockv1.Session, error) {
				res, err := c.Identity.SignInWithApple(ctx, connect.NewRequest(&sockv1.SignInWithAppleRequest{
					IdentityToken:     r.IDToken,
					Nonce:             r.Nonce,
					DeviceLabel:       label,
					AuthorizationCode: r.Code,
					AppleClient:       sockv1.AppleClient_APPLE_CLIENT_WEB,
				}))
				if err != nil {
					return nil, err
				}
				return res.Msg.GetSession(), nil
			})
		},
	}
	f.register(cmd)
	return cmd
}

func newRegisterCmd(app *App) *cobra.Command {
	var f signInFlags
	var invite, handle, name, terms string
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Create a sock account with Sign in with Apple and save a session token",
		Long: "Create a human account, then sign in as login does.\n\n" +
			"--accept-terms says you agree to the terms of service " + termsURL + " and the privacy policy " + privacyURL +
			". This build ships terms version " + termsVersion + ".\n\n" +
			"--invite is required unless the server waives it for your Apple ID.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h := strings.TrimPrefix(handle, "@")
			err := app.signIn(cmd, f, func(ctx context.Context, c Clients, r login.Result, label string) (*sockv1.Session, error) {
				res, err := c.Identity.RegisterWithApple(ctx, connect.NewRequest(&sockv1.RegisterWithAppleRequest{
					IdentityToken:        r.IDToken,
					Nonce:                r.Nonce,
					DeviceLabel:          label,
					InviteCode:           invite,
					Handle:               h,
					DisplayName:          name,
					AuthorizationCode:    r.Code,
					AcceptedTermsVersion: terms,
					AppleClient:          sockv1.AppleClient_APPLE_CLIENT_WEB,
				}))
				if err != nil {
					return nil, err
				}
				return res.Msg.GetSession(), nil
			})
			if client.Reason(err) == sockv1.ErrorReason_ERROR_REASON_TERMS_NOT_ACCEPTED {
				return &rewritten{fmt.Sprintf("terms version %s is not the server's current one; update sock, then read the terms again at %s", terms, termsURL), err}
			}
			return err
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&invite, "invite", "", "invite code")
	cmd.Flags().StringVar(&handle, "handle", "", "handle, lowercase a-z, 0-9 and _, 3 to 20 characters (required)")
	cmd.Flags().StringVar(&name, "name", "", "display name (required)")
	cmd.Flags().StringVar(&terms, "accept-terms", "", "the terms version you accept, "+termsVersion+" in this build (required)")
	for _, req := range []string{"handle", "name", "accept-terms"} {
		_ = cmd.MarkFlagRequired(req) // the flag exists: registered above
	}
	return cmd
}

// signIn runs the browser handoff, then rpc, then writes the session token.
func (a *App) signIn(cmd *cobra.Command, f signInFlags, rpc signInRPC) error {
	cfg, err := a.load(true)
	if err != nil {
		return err
	}
	if a.ConfigPath == "" {
		return errors.New("no config file path: set HOME or XDG_CONFIG_HOME")
	}
	if existing, err := config.FileToken(a.ConfigPath); err != nil {
		return err
	} else if existing != "" {
		return a.tokenExists()
	}
	if why, err := config.PuppetFileConflict(a.ConfigPath, cfg.URL, cfg.AccessGate); err != nil {
		return err
	} else if why != "" {
		return errors.New(why)
	}
	label, err := a.deviceLabel(f.label)
	if err != nil {
		return err
	}
	stderr := cmd.ErrOrStderr()
	if a.Getenv("SOCK_TOKEN") != "" {
		warn(stderr, "SOCK_TOKEN is set and overrides the token sock login saves; unset it to use the new session")
	}
	res, err := login.Run(cmd.Context(), login.Options{
		Hub:      cfg.URL,
		Port:     f.port,
		Timeout:  a.LoginTimeout,
		Show:     a.showLoginURL(stderr, f.noBrowser),
		Received: func() { warn(stderr, "sign-in received") },
	})
	if err != nil {
		return err
	}
	cfg.Token = "" // sign-in is anonymous: never send another token along
	c := newClients(cfg, a.userAgent())
	session, err := call(cmd.Context(), a, func(ctx context.Context) (*sockv1.Session, error) {
		return rpc(ctx, c, res, label)
	})
	if err != nil {
		return err
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
	a.keepMachineKey(cmd, cfg, session)
	session.Token = ""
	if a.jsonOut {
		return a.printer().json(cmd.OutOrStdout(), session)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "signed in as @%s on device %s\n", line(session.GetAccount().GetHuman().GetHandle()), line(session.GetDevice().GetId()))
	return err
}

// keepMachineKey mints a machine key for this host with the login just made
// and keeps it as machine_key, unless the file already has one: sign in once
// and sock-mcp makes each project its own puppet. The login stands either
// way: a key that cannot be minted or saved only says how to finish.
func (a *App) keepMachineKey(cmd *cobra.Command, cfg config.Config, session *sockv1.Session) {
	stderr := cmd.ErrOrStderr()
	finish := "sign-in is saved; to let sock-mcp make a puppet per project, run: sock machine-key create | sock machine-key use"
	if fc, err := client.LoadFileConfig(a.ConfigPath); err == nil && strings.TrimSpace(fc.MachineKey) != "" {
		return
	}
	cfg.Token = session.GetToken()
	short, _, _ := strings.Cut(a.host(), ".")
	label, err := labelOr("", short)
	if err != nil {
		warnf(stderr, "no machine key kept: %v; %s", err, finish)
		return
	}
	res, err := unary(cmd.Context(), a, newClients(cfg, a.userAgent()).Identity.MintMachineKey, &sockv1.MintMachineKeyRequest{Label: label})
	if err != nil {
		warnf(stderr, "no machine key kept (%v); %s", client.Describe(err), finish)
		return
	}
	if err := config.SetMachineKey(a.ConfigPath, res.GetToken(), cfg.URL, cfg.AccessGate, false); err != nil {
		warnf(stderr, "no machine key kept (%v); %s", err, finish)
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

// showLoginURL opens the browser, or prints the URL when asked to or when
// no browser opens. The URL is printed only then: it carries the state, and
// anyone who reads it can finish a login into their own account.
func (a *App) showLoginURL(stderr io.Writer, noBrowser bool) func(string) error {
	return func(u string) error {
		if !noBrowser {
			if err := a.OpenBrowser(u); err == nil {
				_, err := fmt.Fprintln(stderr, "Opened your browser to sign in with Apple; waiting for it to finish.")
				return err
			}
			warn(stderr, "could not open a browser")
		}
		_, err := fmt.Fprintf(stderr, "Open this URL in a browser on this computer to sign in with Apple:\n\n  %s\n\n", line(u))
		return err
	}
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
		Args:  cobra.NoArgs,
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
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "signed out")
			return err
		},
	}
}
