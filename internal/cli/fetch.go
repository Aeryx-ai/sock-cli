package cli

import (
	"context"
	"errors"
	"slices"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

// fetchBatch is the FetchPending limit per call (spec allows 1 to 500).
const fetchBatch = 100

func newFetchCmd(app *App) *cobra.Command {
	var noAck bool
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Print pending messages and acknowledge them",
		Long:  "Print every pending message, acknowledging each batch after it is printed. With --no-ack, print one batch and leave it pending.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			p := app.printer()
			// A batch of only ids already printed means acks are not draining
			// the queue; stop rather than print the same messages forever.
			seen := map[string]bool{}
			for {
				msgs, err := call(cmd.Context(), app, func(ctx context.Context) ([]*sockv1.Message, error) {
					res, err := c.Messaging.FetchPending(ctx, connect.NewRequest(&sockv1.FetchPendingRequest{Limit: fetchBatch}))
					if err != nil {
						return nil, err
					}
					return res.Msg.GetMessages(), nil
				})
				if err != nil {
					return err
				}
				if len(msgs) == 0 {
					return nil
				}
				if !slices.ContainsFunc(msgs, func(m *sockv1.Message) bool { return !seen[m.GetId()] }) {
					return errors.New("the server returned messages already printed and acknowledged; stopping")
				}
				ids := make([]string, 0, len(msgs))
				for _, m := range msgs {
					if err := p.Message(cmd.OutOrStdout(), m); err != nil {
						return err
					}
					ids = append(ids, m.GetId())
					seen[m.GetId()] = true
				}
				if noAck {
					// Unacked messages come back on the next fetch; looping would
					// print the same batch forever.
					return nil
				}
				if err := ack(cmd.Context(), app, c, ids); err != nil {
					return err
				}
			}
		},
	}
	cmd.Flags().BoolVar(&noAck, "no-ack", false, "print one batch and leave it pending")
	return cmd
}

// ack acknowledges ids, retrying transient failures. Acks are idempotent.
func ack(ctx context.Context, app *App, c Clients, ids []string) error {
	_, err := call(ctx, app, func(ctx context.Context) (struct{}, error) {
		_, err := c.Messaging.Ack(ctx, connect.NewRequest(&sockv1.AckRequest{MessageIds: ids}))
		return struct{}{}, err
	})
	return err
}

// maxAck is Ack's id limit.
const maxAck = 500

func newAckCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "ack <message id>...",
		Short: "Acknowledge messages, 1 to 500",
		Args:  cobra.RangeArgs(1, maxAck),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			return ack(cmd.Context(), app, c, args)
		},
	}
}
