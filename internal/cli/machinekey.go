package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"
	client "github.com/guygrigsby/sock-client"

	"github.com/aeryx-ai/sock-cli/internal/config"
)

func newMachineKeyCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "machine-key", Short: "Keys that let a machine make its own project puppets"}
	var label string
	create := &cobra.Command{
		Use:   "create",
		Short: "Mint a machine key for this machine",
		Long: "Mint a machine key (sock_mk_…): a credential that may create, list and erase the puppets it makes, and " +
			"nothing else. It is printed alone on stdout, once, so it pipes; the device id goes to stderr. Needs your " +
			"login. Revoke an unused one with sock devices revoke.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			short, _, _ := strings.Cut(app.host(), ".")
			label, err := labelOr(label, short)
			if err != nil {
				return err
			}
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.MintMachineKey, &sockv1.MintMachineKeyRequest{Label: label})
			if err != nil {
				return err
			}
			if app.jsonOut {
				return app.printer().json(cmd.OutOrStdout(), res)
			}
			return printToken(cmd, res.GetToken(), res.GetDevice().GetId())
		},
	}
	create.Flags().StringVar(&label, "label", "", "what the key is for (default this machine's short hostname)")
	cmd.AddCommand(create, newMachineKeyUseCmd(app))
	return cmd
}

// maxKeyInput bounds what machine-key use reads from stdin.
const maxKeyInput = 4096

// newMachineKeyUseCmd keeps a machine key in the config file as machine_key,
// where sock-mcp reads it when its environment has none. The key comes on
// stdin, never argv (visible in ps), and is never printed.
func newMachineKeyUseCmd(app *App) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "use",
		Short: "Keep a machine key from stdin for sock-mcp",
		Long: "Read a machine key (sock_mk_…) from stdin and write it to the config file as machine_key (mode 600), where " +
			"sock-mcp reads it when SOCK_TOKEN is not set (a Codex plugin starts it without your environment) and makes " +
			"each project its own puppet. The key is checked with the server first and saved with its url. sock-mcp takes SOCK_TOKEN first, then machine_key, then puppet_token. Pipe it: " +
			"sock machine-key create | sock machine-key use. Another machine_key in the file needs --force. " +
			"The key is not printed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if app.StdinIsTTY() {
				return errors.New("pipe the key in, so it is not echoed: sock machine-key create | sock machine-key use")
			}
			// one byte past the limit, so a longer input is refused, not cut
			b, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxKeyInput+1))
			if err != nil {
				return fmt.Errorf("read the machine key: %w", err)
			}
			key := strings.TrimSpace(string(b))
			if len(b) > maxKeyInput || client.TokenKind(key) != client.KindMachineKey || strings.ContainsFunc(key, unicode.IsSpace) {
				return errors.New("stdin is not one machine key (sock_mk_…); mint one with sock machine-key create")
			}
			cfg, err := app.load(true)
			if err != nil {
				return err
			}
			// before the key leaves: never to a server the file does not name
			if why, err := config.PuppetFileConflict(app.ConfigPath, cfg.URL, cfg.AccessGate); err != nil {
				return err
			} else if why != "" {
				return errors.New(why)
			}
			// sock-mcp sends the key to the url saved with it: a key from
			// another hub must not be saved beside this one's url
			cfg.Token = key
			if _, err := unary(cmd.Context(), app, newClients(cfg, app.userAgent()).Identity.ListPuppets, &sockv1.ListPuppetsRequest{}); connect.CodeOf(err) == connect.CodeUnauthenticated {
				return errors.New("the server this command is using does not accept this machine key; mint one there with sock machine-key create, or set SOCK_URL for both commands")
			} else if err != nil {
				return err
			}
			if err := config.SetMachineKey(app.ConfigPath, key, cfg.URL, cfg.AccessGate, force); errors.Is(err, config.ErrMachineKeyExists) {
				return fmt.Errorf("%s holds another machine_key; pass --force to replace it", app.ConfigPath)
			} else if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "saved machine key to %s\n", app.ConfigPath)
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace another machine_key in the config file")
	return cmd
}

// defaultEnsureTimeout bounds puppets ensure; sweepTimeout the sweep within it.
const (
	defaultEnsureTimeout = 60 * time.Second
	sweepTimeout         = 20 * time.Second
)

// newPuppetEnsureCmd makes sure dir's project has a puppet and says where its
// token is kept. The token itself is never printed: a hook reads the file.
func newPuppetEnsureCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "ensure <dir>",
		Short: "Make the project's puppet with your machine key and print where its token is kept",
		Long: "With a machine key as the token (SOCK_TOKEN=sock_mk_…), make the puppet for the project that holds <dir> " +
			"(the repository, every worktree sharing one), named \"<project> on <host>\", or reuse the cached one. " +
			"Puppets of repositories that no longer exist are erased first. Prints the path of the file that holds the " +
			"puppet's token (mode 600); the token is not printed.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := app.load(false)
			if err != nil {
				return err
			}
			if client.TokenKind(cfg.Token) != client.KindMachineKey {
				return errors.New("puppets ensure needs a machine key (sock_mk_…) as the token: mint one with sock machine-key create")
			}
			c, err := app.clients()
			if err != nil {
				return err
			}
			p := &client.Projects{
				API:  client.NewPuppetAPI(c, app.Retrier),
				Dir:  client.ProjectsDir(app.Getenv, cfg.Config),
				Host: client.ShortHost(),
				Log:  func(f string, a ...any) { _, _ = fmt.Fprintln(cmd.ErrOrStderr(), line(fmt.Sprintf(f, a...))) },
			}
			// a cached token the hub rejects (the puppet was erased) is replaced
			p.Check = client.NewTokenCheck(cfg.Config, app.userAgent())
			// the whole run is bounded: it holds the project's cache lock, and a
			// hook waits on it
			limit := cmp.Or(app.EnsureTimeout, defaultEnsureTimeout)
			ctx, cancel := context.WithTimeout(cmd.Context(), limit)
			defer cancel()
			gaveUp := func(err error) error {
				if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
					return fmt.Errorf("puppets ensure gave up after %s: %w", limit, err)
				}
				return err
			}
			// the path first: a token check that runs out the clock still
			// keeps the cached token, and its path must be printable
			path, err := p.TokenFile(ctx, args[0])
			if err != nil {
				return gaveUp(err)
			}
			sweep, cancelSweep := context.WithTimeout(ctx, sweepTimeout)
			p.Sweep(sweep)
			cancelSweep()
			if _, err := p.Ensure(ctx, args[0]); err != nil {
				return gaveUp(err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		},
	}
}
