package cli

import (
	"context"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

func newWhoamiCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the account this token belongs to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			acct, err := call(cmd.Context(), app, func(ctx context.Context) (*sockv1.Account, error) {
				res, err := c.Identity.GetMe(ctx, connect.NewRequest(&sockv1.GetMeRequest{}))
				if err != nil {
					return nil, err
				}
				return res.Msg.GetAccount(), nil
			})
			if err != nil {
				return err
			}
			return app.printer().Account(cmd.OutOrStdout(), acct)
		},
	}
}
