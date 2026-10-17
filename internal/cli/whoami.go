package cli

import (
	"cmp"
	"fmt"

	"github.com/spf13/cobra"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"
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
			me, err := unary(cmd.Context(), app, c.Identity.GetMe, &sockv1.GetMeRequest{})
			if err != nil {
				return err
			}
			p := app.printer()
			if p.JSON {
				return p.json(cmd.OutOrStdout(), me)
			}
			if err := p.Account(cmd.OutOrStdout(), me.GetAccount()); err != nil {
				return err
			}
			if me.GetAccount().GetHuman() == nil {
				return nil
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), termsLine(me))
			return err
		},
	}
}

// termsLine is "terms accepted <v>, current <v>".
func termsLine(me *sockv1.GetMeResponse) string {
	return fmt.Sprintf("terms accepted %s, current %s", line(cmp.Or(me.GetAcceptedTermsVersion(), "none")), line(me.GetCurrentTermsVersion()))
}

func newTermsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "terms",
		Short: "Show the terms version you accepted and the current one",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			me, err := unary(cmd.Context(), app, c.Identity.GetMe, &sockv1.GetMeRequest{})
			if err != nil {
				return err
			}
			if app.jsonOut {
				return app.printer().json(cmd.OutOrStdout(), me)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "accepted %s, current %s\n%s\n", line(cmp.Or(me.GetAcceptedTermsVersion(), "none")), line(me.GetCurrentTermsVersion()), termsURL)
			return err
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "accept <version>",
		Short: "Accept the terms of service at " + termsURL,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.AcceptTerms, &sockv1.AcceptTermsRequest{TermsVersion: args[0]})
			if err != nil {
				return err
			}
			return app.printer().Done(cmd.OutOrStdout(), res, "accepted terms "+args[0])
		},
	})
	return cmd
}
