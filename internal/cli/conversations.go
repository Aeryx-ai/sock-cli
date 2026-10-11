package cli

import (
	"context"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

func newConversationsCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "conversations",
		Short: "List conversations this account is in",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			convs, err := call(cmd.Context(), app, func(ctx context.Context) ([]*sockv1.Conversation, error) {
				res, err := c.Messaging.ListConversations(ctx, connect.NewRequest(&sockv1.ListConversationsRequest{}))
				if err != nil {
					return nil, err
				}
				return res.Msg.GetConversations(), nil
			})
			if err != nil {
				return err
			}
			p := app.printer()
			for _, conv := range convs {
				if err := p.Conversation(cmd.OutOrStdout(), conv); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
