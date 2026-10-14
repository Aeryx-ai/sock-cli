package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	client "github.com/guygrigsby/sock-client"
	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
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
	create.Flags().StringVar(&label, "label", "", "what the key is for, usually this host (required)")
	_ = create.MarkFlagRequired("label") // the flag exists: registered above
	cmd.AddCommand(create)
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
				Log:  func(f string, a ...any) { _, _ = fmt.Fprintf(cmd.ErrOrStderr(), f+"\n", a...) },
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
