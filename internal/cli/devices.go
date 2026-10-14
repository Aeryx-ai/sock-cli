package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"

	"github.com/guygrigsby/sock-cli/internal/config"
)

// tokenDeviceID is the device id inside a sock_st_ or sock_pt_ token.
func tokenDeviceID(token string) string {
	for _, prefix := range []string{humanTokenPrefix, puppetTokenPrefix} {
		if rest, ok := strings.CutPrefix(token, prefix); ok {
			id, _, _ := strings.Cut(rest, "_")
			return id
		}
	}
	return ""
}

func newDevicesCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "List your devices; the one calling is marked (this)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.ListDevices, &sockv1.ListDevicesRequest{})
			if err != nil {
				return err
			}
			this := tokenDeviceID(app.token)
			p := app.printer()
			for _, d := range res.GetDevices() {
				if err := p.Device(cmd.OutOrStdout(), d, d.GetId() == this, ""); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "revoke <device id>",
		Short: "Revoke one of your devices or a puppet's token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.RevokeDevice, &sockv1.RevokeDeviceRequest{DeviceId: args[0]})
			if err != nil {
				return err
			}
			return app.printer().Done(cmd.OutOrStdout(), res, "revoked "+args[0])
		},
	})
	return cmd
}

func newPuppetsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "puppets",
		Short: "List your puppets and their tokens' devices",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.ListPuppets, &sockv1.ListPuppetsRequest{})
			if err != nil {
				return err
			}
			p := app.printer()
			w := cmd.OutOrStdout()
			for _, pw := range res.GetPuppets() {
				if p.JSON {
					if err := p.json(w, pw); err != nil {
						return err
					}
					continue
				}
				if err := p.Account(w, pw.GetPuppet()); err != nil {
					return err
				}
				for _, d := range pw.GetDevices() {
					if err := p.Device(w, d, false, "  "); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	cmd.AddCommand(newPuppetCreateCmd(app), newPuppetTokenCmd(app), newPuppetUseCmd(app), newPuppetEraseCmd(app))
	return cmd
}

func newPuppetCreateCmd(app *App) *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a puppet and print its first token",
		Long:  "Create a puppet. Its token is printed alone on stdout, once, so it pipes; the puppet and device go to stderr.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.CreatePuppet, &sockv1.CreatePuppetRequest{Name: args[0], TokenLabel: label})
			if err != nil {
				return err
			}
			if app.jsonOut {
				return app.printer().json(cmd.OutOrStdout(), res)
			}
			if err := app.printer().Account(cmd.ErrOrStderr(), res.GetPuppet()); err != nil {
				return err
			}
			return printToken(cmd, res.GetToken(), res.GetDevice().GetId())
		},
	}
	cmd.Flags().StringVar(&label, "token-label", "", "label for the first token (required)")
	_ = cmd.MarkFlagRequired("token-label") // the flag exists: registered above
	return cmd
}

func newPuppetTokenCmd(app *App) *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:   "token <puppet id>",
		Short: "Mint another token for a puppet",
		Long:  "Mint a puppet token. It is printed alone on stdout, once, so it pipes; the device id goes to stderr.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.MintPuppetToken, &sockv1.MintPuppetTokenRequest{PuppetId: args[0], Label: label})
			if err != nil {
				return err
			}
			if app.jsonOut {
				return app.printer().json(cmd.OutOrStdout(), res)
			}
			return printToken(cmd, res.GetToken(), res.GetDevice().GetId())
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "token label (required)")
	_ = cmd.MarkFlagRequired("label") // the flag exists: registered above
	return cmd
}

// newPuppetUseCmd mints a token for a puppet and keeps it in the config file
// as puppet_token, where sock-mcp reads it when its environment has none. The
// token is never printed, so it passes through no terminal or shell history.
func newPuppetUseCmd(app *App) *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:   "use <puppet id>",
		Short: "Mint a token for a puppet and keep it for sock-mcp",
		Long: "Mint a puppet token and write it to the config file as puppet_token (mode 600), where sock-mcp reads it when " +
			"SOCK_TOKEN is not set (a Codex plugin starts it without your environment). Your own login token is untouched. " +
			"The token is not printed.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := app.load(false)
			if err != nil {
				return err
			}
			// sock-mcp sends the token to the url in the file, so refuse before minting
			// when that is not the server this command is talking to.
			if why, err := config.PuppetFileConflict(app.ConfigPath, cfg.URL, cfg.AccessGate); err != nil {
				return err
			} else if why != "" {
				return errors.New(why)
			}
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.MintPuppetToken, &sockv1.MintPuppetTokenRequest{PuppetId: args[0], Label: label})
			if err != nil {
				return err
			}
			if err := config.SetPuppetToken(app.ConfigPath, res.GetToken(), cfg.URL, cfg.AccessGate); err != nil {
				return fmt.Errorf("minted a token (device %s) but could not save it: %w; revoke it with sock devices revoke", res.GetDevice().GetId(), err)
			}
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "saved puppet token (device %s) to %s\n", res.GetDevice().GetId(), app.ConfigPath)
			return err
		},
	}
	cmd.Flags().StringVar(&label, "label", "sock-mcp", "token label")
	return cmd
}

// printToken writes the minted token alone on stdout and its device on stderr.
func printToken(cmd *cobra.Command, token, deviceID string) error {
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "device "+deviceID); err != nil {
		return err
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), token)
	return err
}

func newPuppetEraseCmd(app *App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "erase <puppet id>",
		Short: "Erase a puppet, its tokens and its messages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !yes {
				if !app.StdinIsTTY() {
					return fmt.Errorf("erasing puppet %s cannot be undone; pass --yes to confirm off a terminal", id)
				}
				answer, err := ask(cmd, fmt.Sprintf("Erase puppet %s? This cannot be undone. Type yes to confirm: ", id))
				if err != nil {
					return err
				}
				if answer != "yes" {
					return errNotConfirmed
				}
			}
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.ErasePuppet, &sockv1.ErasePuppetRequest{PuppetId: id})
			if err != nil {
				return err
			}
			return app.printer().Done(cmd.OutOrStdout(), res, "erased "+id)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm without asking (required off a terminal)")
	return cmd
}
