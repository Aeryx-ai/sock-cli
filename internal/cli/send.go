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

	client "github.com/guygrigsby/sock-client"
	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

// maxTextBytes is the spec's SendRequest.text limit.
const maxTextBytes = 65536

func newSendCmd(app *App) *cobra.Command {
	var (
		conversation, project, idemKey, session, sessionFlagName string
		asPuppet                                                 bool
	)
	cmd := &cobra.Command{
		Use:   "send [text...]",
		Short: "Send a message to your owner, or to --conversation",
		Long:  "Send a message. Text comes from the arguments, or stdin when there are none. Without --conversation it goes to your owner (puppet tokens only; a human must pass --conversation).",
		RunE: func(cmd *cobra.Command, args []string) error {
			if project != "" && !asPuppet {
				return errors.New("--project needs --as-puppet")
			}
			if session != "" && !asPuppet {
				return errors.New("--session needs --as-puppet")
			}
			if sessionFlagName != "" && session == "" {
				return errors.New("--session-name needs --session")
			}
			if project != "" && session != "" {
				return errors.New("--project and --session name different puppets; pass one")
			}
			var sess client.Session
			if session != "" {
				var err error
				if sess, err = client.ParseSession(session); err != nil {
					return fmt.Errorf("--session: %w", err)
				}
			}
			if cmd.Flags().Changed("idempotency-key") && !validIdempotencyKey(idemKey) {
				return errors.New("--idempotency-key must be 1 to 128 characters of A-Z a-z 0-9 . _ : -, starting with a letter or digit")
			}
			text, err := readText(args, cmd.InOrStdin(), app.StdinIsTTY())
			if err != nil {
				return err
			}
			var (
				c       Clients
				recheck func(context.Context) (Clients, error)
			)
			switch {
			case session != "":
				name, set := sessionName(sess, sessionFlagName)
				c, err = app.sessionPuppetClients(cmd.Context(), sess, name, set, cmd.ErrOrStderr())
			case asPuppet:
				c, recheck, err = app.puppetClients(cmd.Context(), project, true, cmd.ErrOrStderr())
			default:
				c, err = app.clients()
			}
			if err != nil {
				return err
			}
			if conversation == "" && app.human() {
				return errors.New("a human must pass --conversation; without it send goes to a puppet's owner")
			}
			key := idemKey // the caller's, stable across reruns
			if key == "" {
				key = rand.Text() // one key per invocation, reused by every retry
			}
			send := func(ctx context.Context) (*sockv1.Message, error) {
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
			}
			msg, err := call(cmd.Context(), app, send)
			// the cached project puppet token was used unchecked: the puppet
			// was erased, so check, replace and send once more
			if recheck != nil && client.Reason(err) == sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID {
				if c, err = recheck(cmd.Context()); err != nil {
					return err
				}
				msg, err = call(cmd.Context(), app, send)
			}
			if err != nil {
				return err
			}
			return app.printer().Message(cmd.OutOrStdout(), msg)
		},
	}
	cmd.Flags().StringVar(&conversation, "conversation", "", "conversation id (default: your owner)")
	cmd.Flags().BoolVar(&asPuppet, "as-puppet", false, "send as the puppet sock-mcp would: SOCK_TOKEN, else the config file's machine_key (the project's puppet), else its puppet_token; never your login")
	cmd.Flags().StringVar(&idemKey, "idempotency-key", "", "key the hub dedupes this send by (1 to 128 of A-Z a-z 0-9 . _ : -); a rerun with the same key posts at most once (default: a fresh key per invocation)")
	cmd.Flags().StringVar(&session, "session", "", "with --as-puppet, send as this agent session's own puppet, <platform>:<session id> (platform claude-code or codex), made with the machine key on its first send")
	cmd.Flags().StringVar(&sessionFlagName, "session-name", "", "with --session, the session's name; the puppet is renamed when it changes (default: the working directory's name and the first 8 of the session id)")
	cmd.Flags().StringVar(&project, "project", "", "with --as-puppet, the directory sock-mcp starts in (default: the working directory; CLAUDE_PROJECT_DIR wins when set)")
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

// validIdempotencyKey is 1 to 128 shell-safe characters, starting with a
// letter or digit: the hub's key limit, narrowed to one word that can be
// pasted back into a command line as printed.
func validIdempotencyKey(k string) bool {
	if k == "" || len(k) > 128 {
		return false
	}
	for i := range len(k) {
		c := k[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '.' && c != '_' && c != ':' && c != '-') {
			return false
		}
	}
	return true
}
