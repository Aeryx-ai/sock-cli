package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/aeryx-ai/sock-cli/internal/config"
	client "github.com/aeryx-ai/sock-client"
)

// A check's answers: one call to the hub with one credential.
const (
	checkValid     = "valid"
	checkMissing   = "missing"
	checkRejected  = "rejected"
	checkUnchecked = "unchecked"
)

// credentialCheck is one check as the report prints it.
type credentialCheck struct {
	Check  string `json:"check"`
	Handle string `json:"handle,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// checkOf words a check's error.
func checkOf(err error) credentialCheck {
	switch {
	case err == nil:
		return credentialCheck{Check: checkValid}
	case rejected(err):
		return credentialCheck{Check: checkRejected}
	}
	return credentialCheck{Check: checkUnchecked, Reason: err.Error()}
}

func (c credentialCheck) text() string {
	switch {
	case c.Handle != "":
		return c.Check + " @" + c.Handle
	case c.Reason != "":
		return c.Check + " (" + c.Reason + ")"
	}
	return c.Check
}

// settingsReport is what sock settings prints. It never holds a credential.
type settingsReport struct {
	Config       string          `json:"config"`
	URL          string          `json:"url"`
	Session      credentialCheck `json:"session"`
	MachineKey   credentialCheck `json:"machine_key"`
	PuppetToken  string          `json:"puppet_token"`
	MessageOwner struct {
		Value string `json:"value"`
		From  string `json:"from"`
	} `json:"message_owner"`
}

func newSettingsCmd(app *App) *cobra.Command {
	var messageOwner string
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Show this machine's sock settings and check its credentials, or change a setting",
		Long: "Report the config file: its path, the server (url) its credentials belong to, whether the server accepts the " +
			"session (token) and the machine key (valid, missing, rejected or unchecked), whether a puppet_token is present, and " +
			"whether this machine's puppets may message you without you writing first (message_owner, on unless the file or " +
			client.MessageOwnerEnvVar + " says off). No credential is printed. It exits 0 whatever the checks answer.\n\n" +
			"--message-owner=on|off writes message_owner. --url <url> writes url, only while the file holds no token, " +
			"puppet_token or machine_key: a credential never goes to a server that did not issue it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if app.ConfigPath == "" {
				return errors.New("no config file path: set HOME or XDG_CONFIG_HOME")
			}
			on, err := onOff(cmd, messageOwner)
			if err != nil {
				return err
			}
			if app.flagURL != "" {
				if err := config.SetURL(app.ConfigPath, app.flagURL); err != nil {
					return err
				}
			}
			if on != nil {
				if err := config.SetMessageOwner(app.ConfigPath, *on); err != nil {
					return err
				}
			}
			r, err := app.settings(cmd)
			if err != nil {
				return err
			}
			if app.jsonOut {
				b, err := json.Marshal(r)
				if err != nil {
					return fmt.Errorf("encode output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", b)
				return err
			}
			return r.print(cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&messageOwner, "message-owner", "", "on or off: whether this machine's puppets may message you without you writing first")
	return cmd
}

// onOff is --message-owner's value, nil when it was not passed.
func onOff(cmd *cobra.Command, v string) (*bool, error) {
	if !cmd.Flags().Changed("message-owner") {
		return nil, nil
	}
	switch v {
	case "on", "off":
		on := v == "on"
		return &on, nil
	}
	return nil, errors.New("--message-owner is on or off")
}

// settings reads the config file and checks its session and machine key,
// both at once, at the file's server: a credential never goes elsewhere.
func (a *App) settings(cmd *cobra.Command) (settingsReport, error) {
	fc, err := client.LoadFileConfig(a.ConfigPath)
	if err != nil {
		return settingsReport{}, err
	}
	r := settingsReport{Config: a.ConfigPath, URL: fc.URL, PuppetToken: "absent"}
	if r.URL == "" {
		r.URL = config.DefaultURL
	}
	if strings.TrimSpace(fc.PuppetToken) != "" {
		r.PuppetToken = "present"
	}
	on, from := client.MessageOwner(a.Getenv, fc)
	r.MessageOwner.Value, r.MessageOwner.From = "off", from
	if on {
		r.MessageOwner.Value = "on"
	}
	if from == client.MessageOwnerEnv {
		r.MessageOwner.From = client.MessageOwnerEnvVar
	}
	var cfg config.Config
	cfg.URL, cfg.AccessGate = r.URL, strings.TrimSpace(fc.AccessGate)
	token, key := strings.TrimSpace(fc.Token), strings.TrimSpace(fc.MachineKey)
	a.token = token // errors are worded for the session's kind
	ctx := cmd.Context()
	r.Session, r.MachineKey = credentialCheck{Check: checkMissing}, credentialCheck{Check: checkMissing}
	var wg sync.WaitGroup
	if token != "" {
		wg.Go(func() {
			me, err := a.checkSession(ctx, cfg, token)
			r.Session = checkOf(err)
			r.Session.Handle = me.GetAccount().GetHuman().GetHandle()
		})
	}
	if key != "" {
		wg.Go(func() { r.MachineKey = checkOf(a.checkKey(ctx, cfg, key)) })
	}
	wg.Wait()
	return r, nil
}

// print writes the report as text, one setting a line. File and server text
// goes through line.
func (r settingsReport) print(w io.Writer) error {
	rows := [][2]string{
		{"config", r.Config},
		{"url", r.URL},
		{"session", r.Session.text()},
		{"machine_key", r.MachineKey.text()},
		{"puppet_token", r.PuppetToken},
		{"message_owner", r.MessageOwner.Value + " (" + r.MessageOwner.From + ")"},
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(w, "%-14s %s\n", row[0], line(row[1])); err != nil {
			return err
		}
	}
	return nil
}
