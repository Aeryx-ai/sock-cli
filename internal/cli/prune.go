package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/aeryx-ai/sock-cli/internal/config"
	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"
	client "github.com/guygrigsby/sock-client"
)

// newPuppetPruneCmd erases the project puppets no machine key can sweep: those
// a session made, or whose key was revoked. A session cannot
// see a puppet's creator, so it matches by name and asks.
func newPuppetPruneCmd(app *App) *cobra.Command {
	var yes, withTokens bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Erase project puppets no machine key looks after, after confirmation",
		Long: "List your puppets named \"<project> on <host>\" that the config file's machine_key did not make, and " +
			"erase them, their tokens and their messages once you type yes. Those are project puppets from before " +
			"machine keys, or of a revoked key: no key's sweep reaches them. A puppet holding a token is left out " +
			"unless --with-tokens, since it may be in use; so are system and connector puppets. Another machine's " +
			"key cannot be asked, so its puppets match too: read the list. Off a terminal it needs --yes.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			if !app.human() {
				return errors.New("puppets prune needs your login, which sees every puppet: run sock login")
			}
			ctx, stdout, stderr := cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr()
			res, err := unary(ctx, app, c.Identity.ListPuppets, &sockv1.ListPuppetsRequest{})
			if err != nil {
				return err
			}
			keyed, unasked, err := app.machineKeyPuppets(ctx, stderr)
			if err != nil {
				return err
			}
			if unasked {
				if withTokens {
					return errors.New("the config file's machine_key is for another server, so its live puppets cannot be told apart; --with-tokens would include them")
				}
				_, _ = fmt.Fprintln(stderr, "the config file's machine_key is for another server: its puppets cannot be told apart")
			}
			var prune []*sockv1.PuppetWithDevices
			held := 0
			for _, pw := range res.GetPuppets() {
				pu := pw.GetPuppet().GetPuppet()
				if pu == nil || pu.GetSystem() || pu.GetConnector() != nil || keyed[pw.GetPuppet().GetId()] || !client.IsProjectPuppetName(pu.GetName()) {
					continue
				}
				if len(pw.GetDevices()) > 0 && !withTokens {
					held++
					continue
				}
				prune = append(prune, pw)
			}
			if held > 0 {
				_, _ = fmt.Fprintf(stderr, "%d matching puppets hold a token and are left out; --with-tokens includes them\n", held)
			}
			if len(prune) == 0 {
				_, err := fmt.Fprintln(stderr, "no project puppets to prune")
				return err
			}
			p := app.printer()
			for _, pw := range prune {
				if err := printPuppet(p, stdout, pw); err != nil {
					return err
				}
			}
			if !yes {
				if !app.StdinIsTTY() {
					return fmt.Errorf("erasing %d puppets cannot be undone; pass --yes to confirm off a terminal", len(prune))
				}
				answer, err := ask(cmd, fmt.Sprintf("Erase these %d puppets, their tokens and their messages? This cannot be undone. Type yes to confirm: ", len(prune)))
				if err != nil {
					return err
				}
				if answer != "yes" {
					return errNotConfirmed
				}
			}
			failed := 0
			for _, pw := range prune {
				id := pw.GetPuppet().GetId()
				_, err := unary(ctx, app, c.Identity.ErasePuppet, &sockv1.ErasePuppetRequest{PuppetId: id})
				if err != nil && client.Reason(err) != sockv1.ErrorReason_ERROR_REASON_NOT_FOUND {
					_, _ = fmt.Fprintln(stderr, line(fmt.Sprintf("could not erase %s: %v", id, err)))
					failed++
					continue
				}
				if err := p.Done(stdout, &sockv1.ErasePuppetResponse{}, "erased "+id); err != nil {
					return err
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d puppets not erased", failed, len(prune))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm without asking (required off a terminal)")
	cmd.Flags().BoolVar(&withTokens, "with-tokens", false, "include puppets that hold a token")
	return cmd
}

// machineKeyPuppets is the ids of the puppets the config file's machine_key
// made, asked of the key's own server, and only when that is the server this
// command speaks to: a key is never sent anywhere else. unasked is a key for
// another server. No key, or a revoked key (its puppets are the owner's alone
// now, said on stderr), is an empty set.
func (a *App) machineKeyPuppets(ctx context.Context, stderr io.Writer) (ids map[string]bool, unasked bool, err error) {
	session, err := a.load(false)
	if err != nil {
		return nil, false, err
	}
	// the file alone: machine_key, else puppet_token, with the file's server
	cfg, err := client.LoadPuppetConfig(client.PuppetOptions{Getenv: func(string) string { return "" }, ConfigPath: a.ConfigPath})
	if errors.Is(err, client.ErrNoPuppetCredential) || err == nil && client.TokenKind(cfg.Token) != client.KindMachineKey {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !config.SameServer(cfg.URL, session.URL) {
		return nil, true, nil
	}
	list, err := client.NewPuppetAPI(client.New(cfg, a.userAgent()), a.Retrier).ListPuppets(ctx)
	if client.Reason(err) == sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID {
		_, _ = fmt.Fprintln(stderr, "the server refused the config file's machine_key (revoked?): the puppets it made are yours alone now and may match")
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("list the puppets of the config file's machine_key: %w", client.Describe(err))
	}
	ids = map[string]bool{}
	for _, p := range list {
		ids[p.ID] = true
	}
	return ids, false, nil
}
