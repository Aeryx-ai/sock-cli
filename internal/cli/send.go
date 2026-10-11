package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

// maxTextBytes is the spec's SendRequest.text limit.
const maxTextBytes = 65536

func newSendCmd(app *App) *cobra.Command {
	var conversation string
	cmd := &cobra.Command{
		Use:   "send [text...]",
		Short: "Send a message to your owner, or to --conversation",
		Long:  "Send a message. Text comes from the arguments, or stdin when there are none. Without --conversation it goes to your owner (puppet tokens only).",
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := readText(args, cmd.InOrStdin(), app.StdinIsTTY())
			if err != nil {
				return err
			}
			c, err := app.clients()
			if err != nil {
				return err
			}
			key := rand.Text() // one key per invocation, reused by every retry
			msg, err := call(cmd.Context(), app, func(ctx context.Context) (*sockv1.Message, error) {
				if conversation == "" {
					res, err := c.Messaging.NotifyOwner(ctx, connect.NewRequest(&sockv1.NotifyOwnerRequest{Text: text, IdempotencyKey: key}))
					if err != nil {
						return nil, err
					}
					return res.Msg.GetMessage(), nil
				}
				res, err := c.Messaging.Send(ctx, connect.NewRequest(&sockv1.SendRequest{ConversationId: conversation, Text: text, IdempotencyKey: key}))
				if err != nil {
					return nil, err
				}
				return res.Msg.GetMessage(), nil
			})
			if err != nil {
				return err
			}
			return app.printer().Message(cmd.OutOrStdout(), msg)
		},
	}
	cmd.Flags().StringVar(&conversation, "conversation", "", "conversation id (default: your owner)")
	return cmd
}

// readText takes the message from args, else stdin. It refuses to block on a
// terminal and validates the spec's limits before any network call.
func readText(args []string, stdin io.Reader, isTTY bool) (string, error) {
	var text string
	switch {
	case len(args) > 0:
		text = strings.Join(args, " ")
	case isTTY:
		return "", errors.New("no text: pass it as arguments or pipe it on stdin")
	default:
		b, err := io.ReadAll(io.LimitReader(stdin, maxTextBytes+2))
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		text = strings.TrimSuffix(string(b), "\n")
	}
	if text == "" {
		return "", errors.New("message is empty")
	}
	if len(text) > maxTextBytes {
		return "", fmt.Errorf("message is longer than %d bytes", maxTextBytes)
	}
	if !utf8.ValidString(text) {
		return "", errors.New("message is not valid UTF-8")
	}
	return text, nil
}
