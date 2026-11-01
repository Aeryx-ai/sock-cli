// Package cli defines the sock command tree.
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"

	"github.com/aeryx-ai/sock-cli/internal/config"
	client "github.com/guygrigsby/sock-client"
)

// App holds everything commands depend on, so tests can substitute any of it.
type App struct {
	Version    string
	Getenv     func(string) string
	ConfigPath string
	StdinIsTTY func() bool
	Retrier    client.Retrier
	// RetryBudget bounds the time one call spends retrying, whatever the
	// server's Retry-After says. Zero means defaultRetryBudget.
	RetryBudget time.Duration
	// Sleep is used between listen reconnects.
	Sleep func(context.Context, time.Duration) error
	// Jitter spreads a reconnect backoff so many clients do not retry in
	// lockstep. It must return a value in [0, d]. Nil means no jitter.
	Jitter func(d time.Duration) time.Duration
	// ListenIdle is how long listen waits with no message or heartbeat
	// before it treats the stream as dead and reconnects. Zero means
	// defaultListenIdle.
	ListenIdle time.Duration
	// RunExec runs a listen --exec command with the message on stdin.
	RunExec func(ctx context.Context, cmdline string, stdin []byte, env []string, stdout, stderr io.Writer) error

	// OpenBrowser opens a URL in the person's browser (sock login).
	OpenBrowser func(url string) error
	// Hostname names this machine in the default device label.
	Hostname func() (string, error)
	// LoginTimeout bounds the wait for the browser sign-in. Zero means
	// login.DefaultTimeout.
	LoginTimeout time.Duration
	// EnsureTimeout bounds puppets ensure. Zero means defaultEnsureTimeout.
	EnsureTimeout time.Duration

	flagURL   string
	flagToken string
	jsonOut   bool
	// token is the one the last clients() call resolved; it decides whether
	// errors are worded for a human or a puppet.
	token string
	// url is the server that same call resolved.
	url string
}

// DefaultApp wires the real environment.
func DefaultApp(version string) *App {
	path, err := config.DefaultPath(os.Getenv)
	if err != nil {
		path = "" // no HOME: env and flags still work
	}
	return &App{
		Version:    version,
		Getenv:     os.Getenv,
		ConfigPath: path,
		StdinIsTTY: func() bool {
			fi, err := os.Stdin.Stat()
			return err == nil && fi.Mode()&os.ModeCharDevice != 0
		},
		Retrier:     client.DefaultRetrier(),
		Sleep:       client.SleepCtx,
		Jitter:      equalJitter,
		RunExec:     runShell,
		OpenBrowser: openBrowser,
		Hostname:    os.Hostname,
	}
}

// equalJitter returns a random duration in [d/2, d]: half the backoff is
// kept so a reconnect never degenerates into an immediate retry.
func equalJitter(d time.Duration) time.Duration {
	half := d / 2
	return half + rand.N(d-half+1)
}

// runShell runs cmdline with /bin/sh -c, in an environment without sock's
// credentials: a prompt-injected child must not read the token or gate.
func runShell(ctx context.Context, cmdline string, stdin []byte, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdline)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = append(client.ScrubSecrets(os.Environ()), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (a *App) load(anonymous bool) (config.Config, error) {
	return config.Load(config.Sources{
		FlagURL:   a.flagURL,
		FlagToken: a.flagToken,
		Getenv:    a.Getenv,
		Path:      a.ConfigPath,
		Anonymous: anonymous,
	})
}

func (a *App) userAgent() string { return "sock-cli/" + a.Version }

// clients loads the config, which must hold a token, and builds clients.
func (a *App) clients() (Clients, error) {
	cfg, err := a.load(false)
	if err != nil {
		return Clients{}, err
	}
	a.token, a.url = cfg.Token, cfg.URL
	return newClients(cfg, a.userAgent()), nil
}

// human reports whether the resolved token is a human session.
func (a *App) human() bool { return strings.HasPrefix(a.token, humanTokenPrefix) }

func (a *App) printer() Printer { return Printer{JSON: a.jsonOut} }

// call runs one unary RPC with retries on transient failures, a per-attempt
// timeout, and errors rewritten into actionable text.
func call[T any](ctx context.Context, a *App, fn func(context.Context) (T, error)) (T, error) {
	budget := a.RetryBudget
	if budget <= 0 {
		budget = defaultRetryBudget
	}
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	out, err := client.Call(bctx, a.Retrier, fn)
	// a rate limit is not an outage: client.Describe already names the wait
	limited := client.Reason(err) == sockv1.ErrorReason_ERROR_REASON_RATE_LIMITED
	if ctx.Err() == nil && (client.Transient(err) && !limited || errors.Is(err, context.DeadlineExceeded) && bctx.Err() != nil) {
		if bctx.Err() != nil {
			return out, &rewritten{fmt.Sprintf("the server is unavailable: gave up after %s", budget), err}
		}
		return out, &rewritten{fmt.Sprintf("the server is unavailable: gave up after %d attempts (%s)", max(a.Retrier.Attempts, 1), err), err}
	}
	return out, a.describe(err)
}

// defaultRetryBudget is the longest one call keeps retrying.
const defaultRetryBudget = 60 * time.Second

// NewRoot builds the command tree.
func NewRoot(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:           "sock",
		Short:         "Message your owner and conversations from scripts, jobs and agents",
		Version:       app.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&app.flagURL, "url", "", "server URL (default $SOCK_URL, then "+config.DefaultURL+")")
	pf.StringVar(&app.flagToken, "token", "", "bearer token (default $SOCK_TOKEN; a flag is visible in ps, prefer the env var)")
	pf.BoolVar(&app.jsonOut, "json", false, "print as JSON lines")
	root.AddCommand(newLoginCmd(app), newRegisterCmd(app), newLogoutCmd(app),
		newWhoamiCmd(app), newTermsCmd(app), newNameCmd(app), newHandleCmd(app), newAccountsCmd(app), newAccountCmd(app),
		newDevicesCmd(app), newPuppetsCmd(app), newMachineKeyCmd(app),
		newConversationsCmd(app), newSendCmd(app), newListenCmd(app), newFetchCmd(app), newAckCmd(app),
		newBlockCmd(app), newUnblockCmd(app), newBlocksCmd(app), newReportCmd(app), newExportCmd(app))
	return root
}

// unary runs one unary RPC through call, building a fresh request per
// attempt.
func unary[Req, Res any](ctx context.Context, a *App, fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), req *Req) (*Res, error) {
	return call(ctx, a, func(ctx context.Context) (*Res, error) {
		res, err := fn(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	})
}
