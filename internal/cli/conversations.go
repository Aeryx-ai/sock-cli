package cli

import (
	"github.com/spf13/cobra"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

// maxGroupOthers is how many accounts CreateGroup takes besides the caller.
const maxGroupOthers = 19

func newConversationsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conversations",
		Short: "List conversations this account is in",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Messaging.ListConversations, &sockv1.ListConversationsRequest{})
			if err != nil {
				return err
			}
			p := app.printer()
			for _, conv := range res.GetConversations() {
				if err := p.Conversation(cmd.OutOrStdout(), conv); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "direct <account>",
		Short: "Open the direct conversation with a human or one of your puppets",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			peer, err := resolveAccount(cmd.Context(), app, c, args[0])
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Messaging.CreateDirect, &sockv1.CreateDirectRequest{PeerId: peer})
			if err != nil {
				return err
			}
			return app.printer().Conversation(cmd.OutOrStdout(), res.GetConversation())
		},
	}, &cobra.Command{
		Use:   "group [<account>...]",
		Short: "Create a group with up to 19 other accounts",
		Args:  cobra.MaximumNArgs(maxGroupOthers),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			ids, err := resolveAccounts(cmd.Context(), app, c, args)
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Messaging.CreateGroup, &sockv1.CreateGroupRequest{MemberIds: ids})
			if err != nil {
				return err
			}
			return app.printer().Conversation(cmd.OutOrStdout(), res.GetConversation())
		},
	}, &cobra.Command{
		Use:   "add <conversation id> <account>",
		Short: "Add a member to a group",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			id, err := resolveAccount(cmd.Context(), app, c, args[1])
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Messaging.AddMember, &sockv1.AddMemberRequest{ConversationId: args[0], AccountId: id})
			if err != nil {
				return err
			}
			return app.printer().Conversation(cmd.OutOrStdout(), res.GetConversation())
		},
	}, &cobra.Command{
		Use:   "remove <conversation id> <account>",
		Short: "Remove one of your puppets from a group",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			id, err := resolveAccount(cmd.Context(), app, c, args[1])
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Messaging.RemoveMember, &sockv1.RemoveMemberRequest{ConversationId: args[0], AccountId: id})
			if err != nil {
				return err
			}
			return app.printer().Conversation(cmd.OutOrStdout(), res.GetConversation())
		},
	}, &cobra.Command{
		Use:   "leave <conversation id>",
		Short: "Leave a group (puppet tokens too)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Messaging.Leave, &sockv1.LeaveRequest{ConversationId: args[0]})
			if err != nil {
				return err
			}
			return app.printer().Done(cmd.OutOrStdout(), res, "left "+args[0])
		},
	})
	return cmd
}
