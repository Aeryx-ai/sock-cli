package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"
)

// maxGetAccounts is GetAccounts' id limit.
const maxGetAccounts = 100

// resolveAccount turns an <account> argument into an account id: an id as
// given, or @handle looked up (humans only).
func resolveAccount(ctx context.Context, a *App, c Clients, arg string) (string, error) {
	handle, ok := strings.CutPrefix(arg, "@")
	if !ok {
		return arg, nil
	}
	res, err := unary(ctx, a, c.Identity.LookupHandle, &sockv1.LookupHandleRequest{Handle: handle})
	if err != nil {
		return "", fmt.Errorf("@%s: %w", handle, err)
	}
	return res.GetAccount().GetId(), nil
}

func resolveAccounts(ctx context.Context, a *App, c Clients, args []string) ([]string, error) {
	ids := make([]string, 0, len(args))
	for _, arg := range args {
		id, err := resolveAccount(ctx, a, c, arg)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func newNameCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "name", Short: "Change your display name"}
	cmd.AddCommand(&cobra.Command{
		Use:   "set <display name>",
		Short: "Set your display name (puppet tokens too)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.UpdateDisplayName, &sockv1.UpdateDisplayNameRequest{DisplayName: strings.Join(args, " ")})
			if err != nil {
				return err
			}
			return app.printer().Account(cmd.OutOrStdout(), res.GetAccount())
		},
	})
	return cmd
}

func newHandleCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "handle", Short: "Change your handle"}
	cmd.AddCommand(&cobra.Command{
		Use:   "set <handle>",
		Short: "Set your handle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.ChangeHandle, &sockv1.ChangeHandleRequest{Handle: strings.TrimPrefix(args[0], "@")})
			if err != nil {
				return err
			}
			return app.printer().Account(cmd.OutOrStdout(), res.GetAccount())
		},
	})
	return cmd
}

func newAccountsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "accounts", Short: "Look up accounts"}
	cmd.AddCommand(&cobra.Command{
		Use:   "get <id>...",
		Short: "Show accounts by id, 1 to 100",
		Args:  cobra.RangeArgs(1, maxGetAccounts),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.GetAccounts, &sockv1.GetAccountsRequest{Ids: args})
			if err != nil {
				return err
			}
			p := app.printer()
			for _, a := range res.GetAccounts() {
				if err := p.Account(cmd.OutOrStdout(), a); err != nil {
					return err
				}
			}
			return nil
		},
	}, &cobra.Command{
		Use:   "lookup <handle>",
		Short: "Find a human by handle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.LookupHandle, &sockv1.LookupHandleRequest{Handle: strings.TrimPrefix(args[0], "@")})
			if err != nil {
				return err
			}
			return app.printer().Account(cmd.OutOrStdout(), res.GetAccount())
		},
	})
	return cmd
}
