package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"

	"github.com/aeryx-ai/sock-cli/internal/config"
)

// maxEvidence is FileReport's evidence limit.
const maxEvidence = 20

var reportReasons = map[string]sockv1.ReportReason{
	"spam":           sockv1.ReportReason_REPORT_REASON_SPAM,
	"harassment":     sockv1.ReportReason_REPORT_REASON_HARASSMENT,
	"hate":           sockv1.ReportReason_REPORT_REASON_HATE,
	"sexual-content": sockv1.ReportReason_REPORT_REASON_SEXUAL_CONTENT,
	"violence":       sockv1.ReportReason_REPORT_REASON_VIOLENCE,
	"self-harm":      sockv1.ReportReason_REPORT_REASON_SELF_HARM,
	"impersonation":  sockv1.ReportReason_REPORT_REASON_IMPERSONATION,
	"other":          sockv1.ReportReason_REPORT_REASON_OTHER,
}

func reasonNames() string {
	names := make([]string, 0, len(reportReasons))
	for n := range reportReasons {
		names = append(names, n)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

func newBlockCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "block <account>",
		Short: "Stop receiving messages from an account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			id, err := resolveAccount(cmd.Context(), app, c, args[0])
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Messaging.BlockAccount, &sockv1.BlockAccountRequest{AccountId: id})
			if err != nil {
				return err
			}
			return app.printer().Done(cmd.OutOrStdout(), res, "blocked "+id)
		},
	}
}

func newUnblockCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "unblock <account>",
		Short: "Receive messages from an account again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			id, err := resolveAccount(cmd.Context(), app, c, args[0])
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Messaging.UnblockAccount, &sockv1.UnblockAccountRequest{AccountId: id})
			if err != nil {
				return err
			}
			return app.printer().Done(cmd.OutOrStdout(), res, "unblocked "+id)
		},
	}
}

func newBlocksCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "blocks",
		Short: "List the accounts you blocked",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Messaging.ListBlockedAccounts, &sockv1.ListBlockedAccountsRequest{})
			if err != nil {
				return err
			}
			p := app.printer()
			for _, b := range res.GetBlocked() {
				if err := p.Blocked(cmd.OutOrStdout(), b); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newReportCmd(app *App) *cobra.Command {
	var reason, note, evidence string
	cmd := &cobra.Command{
		Use:   "report <account>",
		Short: "Report an account to the operator",
		Long: "Report an account. --evidence names a file of up to 20 messages, one JSON object per line, exactly as " +
			"sock fetch --json or sock listen --json printed them (the evidence tag included).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, ok := reportReasons[reason]
			if !ok {
				return fmt.Errorf("--reason must be one of %s", reasonNames())
			}
			var msgs []*sockv1.ReportedMessage
			if evidence != "" {
				var err error
				if msgs, err = readEvidence(evidence); err != nil {
					return err
				}
			}
			c, err := app.clients()
			if err != nil {
				return err
			}
			subject, err := resolveAccount(cmd.Context(), app, c, args[0])
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Moderation.FileReport, &sockv1.FileReportRequest{SubjectId: subject, Reason: r, Note: note, Evidence: msgs})
			if err != nil {
				return err
			}
			return app.printer().Done(cmd.OutOrStdout(), res, "filed report "+res.GetReportId())
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "one of "+reasonNames()+" (required)")
	cmd.Flags().StringVar(&note, "note", "", "your own words, up to 1000 characters")
	cmd.Flags().StringVar(&evidence, "evidence", "", "file of messages as sock fetch --json printed them")
	_ = cmd.MarkFlagRequired("reason") // the flag exists: registered above
	return cmd
}

// readEvidence reads JSON lines of Message into report evidence.
func readEvidence(path string) ([]*sockv1.ReportedMessage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--evidence: %w", err)
	}
	var out []*sockv1.ReportedMessage
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, len(b)+1) // a message line can exceed the default 64 KiB
	n := 0
	for sc.Scan() {
		n++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m sockv1.Message
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(line, &m); err != nil {
			return nil, fmt.Errorf("--evidence line %d is not a message as sock fetch --json prints it: %w", n, err)
		}
		if len(m.GetEvidenceTag()) == 0 {
			return nil, fmt.Errorf("--evidence line %d has no evidenceTag; use the line exactly as sock fetch --json printed it", n)
		}
		out = append(out, &sockv1.ReportedMessage{
			MessageId:      m.GetId(),
			ConversationId: m.GetConversationId(),
			Seq:            m.GetSeq(),
			SenderId:       m.GetSenderId(),
			SentAt:         m.GetSentAt(),
			Text:           m.GetText(),
			EvidenceTag:    m.GetEvidenceTag(),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("--evidence: %w", err)
	}
	if len(out) > maxEvidence {
		return nil, fmt.Errorf("--evidence holds %d messages; at most %d", len(out), maxEvidence)
	}
	return out, nil
}

func newExportCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "export",
		Short: "Print everything sock holds about you, as one JSON object",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			id, err := unary(cmd.Context(), app, c.Identity.ExportIdentityData, &sockv1.ExportIdentityDataRequest{})
			if err != nil {
				return err
			}
			msg, err := unary(cmd.Context(), app, c.Messaging.ExportMessagingData, &sockv1.ExportMessagingDataRequest{})
			if err != nil {
				return err
			}
			idJSON, err := protojson.Marshal(id)
			if err != nil {
				return fmt.Errorf("encode output: %w", err)
			}
			msgJSON, err := protojson.Marshal(msg)
			if err != nil {
				return fmt.Errorf("encode output: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "{\"identity\":%s,\"messaging\":%s}\n", idJSON, msgJSON)
			return err
		},
	}
}

func newAccountCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{Use: "account", Short: "Manage your own account"}
	var confirm string
	erase := &cobra.Command{
		Use:   "erase",
		Short: "Erase your account, your puppets and their messages",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if confirm == "" && !app.StdinIsTTY() {
				return errors.New("erasing your account cannot be undone; pass --confirm <your handle> off a terminal")
			}
			c, err := app.clients()
			if err != nil {
				return err
			}
			me, err := unary(cmd.Context(), app, c.Identity.GetMe, &sockv1.GetMeRequest{})
			if err != nil {
				return err
			}
			handle := me.GetAccount().GetHuman().GetHandle()
			if handle == "" {
				return errors.New("only a human account can be erased; a puppet is erased by its owner with sock puppets erase")
			}
			typed := strings.TrimPrefix(confirm, "@")
			if confirm == "" {
				if typed, err = ask(cmd, fmt.Sprintf("Erase @%s, its puppets and every message? This cannot be undone. Type your handle to confirm: ", handle)); err != nil {
					return err
				}
				typed = strings.TrimPrefix(typed, "@")
			}
			if typed != handle {
				return errNotConfirmed
			}
			res, err := unary(cmd.Context(), app, c.Identity.EraseAccount, &sockv1.EraseAccountRequest{})
			if err != nil {
				return err
			}
			if err := removeFileTokenIf(app, app.token); err != nil {
				return fmt.Errorf("erased, but %w", err)
			}
			return app.printer().Done(cmd.OutOrStdout(), res, "erased")
		},
	}
	erase.Flags().StringVar(&confirm, "confirm", "", "your handle, to confirm off a terminal")
	cmd.AddCommand(erase)
	return cmd
}

// removeFileTokenIf removes the config file's token when it is token, so a
// token from SOCK_TOKEN never deletes a different one from the file.
func removeFileTokenIf(app *App, token string) error {
	ft, err := config.FileToken(app.ConfigPath)
	if err != nil || ft != token {
		return err
	}
	return config.RemoveToken(app.ConfigPath)
}
