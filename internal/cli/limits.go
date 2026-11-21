package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"
)

// limitKeys names each limit as sock limits prints it and sock limits
// request takes it, in display order.
var limitKeys = []limitKey{
	{"puppets", sockv1.LimitKey_LIMIT_KEY_PUPPETS, (*sockv1.PlanLimits).GetPuppets},
	{"group-members", sockv1.LimitKey_LIMIT_KEY_GROUP_MEMBERS, (*sockv1.PlanLimits).GetGroupMembers},
	{"puppet-sends", sockv1.LimitKey_LIMIT_KEY_PUPPET_SENDS_PER_MINUTE, (*sockv1.PlanLimits).GetPuppetSendsPerMinute},
}

type limitKey struct {
	name  string
	key   sockv1.LimitKey
	value func(*sockv1.PlanLimits) int32
}

// limitKeyName is key's name, or the enum's when this sock predates it.
func limitKeyName(key sockv1.LimitKey) string {
	if i := slices.IndexFunc(limitKeys, func(k limitKey) bool { return k.key == key }); i >= 0 {
		return limitKeys[i].name
	}
	return key.String()
}

var limitStates = map[sockv1.LimitRequestState]string{
	sockv1.LimitRequestState_LIMIT_REQUEST_STATE_PENDING:  "pending",
	sockv1.LimitRequestState_LIMIT_REQUEST_STATE_APPROVED: "approved",
	sockv1.LimitRequestState_LIMIT_REQUEST_STATE_DECLINED: "declined",
}

const maxLimitReason = 500

func newLimitsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "limits",
		Short: "Show your limits and your requests for more",
		Long: "Print your effective limits, one \"<key> <value>\" line each, then your requests for higher ones, " +
			"newest first: \"<key> <requested> <state>\", with \"granted <n>\" when approved. A puppet token sees its " +
			"owner's limits and no requests.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			me, err := unary(cmd.Context(), app, c.Identity.GetMe, &sockv1.GetMeRequest{})
			if err != nil {
				return err
			}
			p, w := app.printer(), cmd.OutOrStdout()
			if err := printLimits(p, w, me.GetLimits()); err != nil {
				return err
			}
			// requests are a human's own: a puppet token is refused them
			if !app.human() {
				return nil
			}
			res, err := unary(cmd.Context(), app, c.Identity.ListMyLimitRequests, &sockv1.ListMyLimitRequestsRequest{})
			if err != nil {
				return err
			}
			for _, r := range res.GetRequests() {
				if err := printLimitRequest(p, w, r); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.AddCommand(newLimitsRequestCmd(app))
	return cmd
}

func newLimitsRequestCmd(app *App) *cobra.Command {
	var reason string
	names := make([]string, len(limitKeys))
	for i, k := range limitKeys {
		names[i] = k.name
	}
	cmd := &cobra.Command{
		Use:   "request <key> <value>",
		Short: "Ask the operator to raise one of your limits",
		Long: "Ask for a higher value of one limit. key is " + strings.Join(names, ", ") + "; value is the number " +
			"you want, above your current limit. One pending request per limit; after a decline, 30 days before " +
			"asking for that limit again. sock limits shows the decision.",
		Args:      cobra.ExactArgs(2),
		ValidArgs: names,
		RunE: func(cmd *cobra.Command, args []string) error {
			i := slices.IndexFunc(limitKeys, func(k limitKey) bool { return k.name == args[0] })
			if i < 0 {
				return fmt.Errorf("unknown limit %q: use %s", args[0], strings.Join(names, ", "))
			}
			value, err := strconv.ParseInt(args[1], 10, 32)
			if err != nil || value <= 0 {
				return fmt.Errorf("value %q is not a positive whole number", args[1])
			}
			if n := utf8.RuneCountInString(reason); n == 0 || n > maxLimitReason {
				return errors.New("--reason must be 1 to 500 characters")
			}
			c, err := app.clients()
			if err != nil {
				return err
			}
			res, err := unary(cmd.Context(), app, c.Identity.RequestLimitIncrease, &sockv1.RequestLimitIncreaseRequest{
				Key: limitKeys[i].key, Requested: int32(value), Reason: reason,
			})
			if err != nil {
				return err
			}
			r := res.GetRequest()
			if app.jsonOut {
				return app.printer().json(cmd.OutOrStdout(), r)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "requested %s %d, %s\n", limitKeyName(r.GetKey()), r.GetRequested(), limitStateName(r.GetState()))
			return err
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why you need more, 1 to 500 characters (required)")
	_ = cmd.MarkFlagRequired("reason") // the flag exists: registered above
	return cmd
}

func limitStateName(s sockv1.LimitRequestState) string {
	if name, ok := limitStates[s]; ok {
		return name
	}
	return s.String()
}

// printLimits is one "<key> <value>" line per limit, or the PlanLimits as
// one json line.
func printLimits(p Printer, w io.Writer, l *sockv1.PlanLimits) error {
	if p.JSON {
		return p.json(w, l)
	}
	for _, k := range limitKeys {
		if _, err := fmt.Fprintf(w, "%s %d\n", k.name, k.value(l)); err != nil {
			return err
		}
	}
	return nil
}

// printLimitRequest is "<key> <requested> <state>[ granted <n>]", or the
// LimitRequest as one json line.
func printLimitRequest(p Printer, w io.Writer, r *sockv1.LimitRequest) error {
	if p.JSON {
		return p.json(w, r)
	}
	text := fmt.Sprintf("%s %d %s", limitKeyName(r.GetKey()), r.GetRequested(), limitStateName(r.GetState()))
	if r.GetState() == sockv1.LimitRequestState_LIMIT_REQUEST_STATE_APPROVED {
		text += fmt.Sprintf(" granted %d", r.GetGranted())
	}
	_, err := fmt.Fprintln(w, text)
	return err
}
