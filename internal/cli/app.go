// Package cli defines the sock command tree.
package cli

import (
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/guygrigsby/sock-cli/internal/config"
	"github.com/guygrigsby/sock-client"
)

// App holds everything commands depend on, so tests can substitute any of it.
type App struct {
	Version    string
	Getenv     func(string) string
	ConfigPath string
	StdinIsTTY func() bool
	Retrier    client.Retrier
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

	flagURL   string
	flagToken string
	jsonOut   bool
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
		Retrier: client.DefaultRetrier(),
		Sleep:   client.SleepCtx,
		Jitter:  equalJitter,
		RunExec: runShell,
	}
}

// equalJitter returns a random duration in [d/2, d]: half the backoff is
// kept so a reconnect never degenerates into an immediate retry.
func equalJitter(d time.Duration) time.Duration {
	half := d / 2
	return half + rand.N(d-half+1)
}

// runShell runs cmdline with /bin/sh -c.
func runShell(ctx context.Context, cmdline string, stdin []byte, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", cmdline)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (a *App) clients() (client.Clients, error) {
	cfg, err := config.Load(config.Sources{
		FlagURL:   a.flagURL,
		FlagToken: a.flagToken,
		Getenv:    a.Getenv,
		Path:      a.ConfigPath,
	})
	if err != nil {
		return client.Clients{}, err
	}
	return client.New(cfg, "sock-cli/"+a.Version), nil
}

func (a *App) printer() Printer { return Printer{JSON: a.jsonOut} }

// call runs one unary RPC with retries on transient failures, a per-attempt
// timeout, and errors rewritten into actionable text.
func call[T any](ctx context.Context, a *App, fn func(context.Context) (T, error)) (T, error) {
	return client.Call(ctx, a.Retrier, fn)
}

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
	root.AddCommand(newWhoamiCmd(app), newSendCmd(app), newFetchCmd(app), newConversationsCmd(app), newListenCmd(app))
	return root
}
