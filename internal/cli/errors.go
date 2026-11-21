package cli

import (
	"errors"

	"connectrpc.com/connect"

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"

	client "github.com/aeryx-ai/sock-client"
)

const (
	humanTokenPrefix  = "sock_st_"
	puppetTokenPrefix = "sock_pt_"
	machineKeyPrefix  = "sock_mk_"
)

// humanText rewrites refusals whose remedy differs for a human session.
var humanText = map[sockv1.ErrorReason]string{
	sockv1.ErrorReason_ERROR_REASON_TOKEN_INVALID:      "the server rejected the session token; run sock login",
	sockv1.ErrorReason_ERROR_REASON_TERMS_NOT_ACCEPTED: "you have not accepted the current terms; run sock terms, then sock terms accept <version>",
}

// reasonText covers the reasons whose text names a sock command: the ones
// sock-client leaves to the caller, because only this CLI has these commands.
var reasonText = map[sockv1.ErrorReason]string{
	sockv1.ErrorReason_ERROR_REASON_NOT_REGISTERED:              "no sock account for that sign-in; sign in with Google, Apple or an emailed code to create one",
	sockv1.ErrorReason_ERROR_REASON_EMAIL_CODE_INVALID:          "no sock account for that sign-in; sign in with Google, Apple or an emailed code to create one",
	sockv1.ErrorReason_ERROR_REASON_ALREADY_REGISTERED:          "this address already has a sock account; run sock login",
	sockv1.ErrorReason_ERROR_REASON_TERMINAL_SIGN_IN_INVALID:    "sign-in timed out; run sock login again",
	sockv1.ErrorReason_ERROR_REASON_REGISTRATION_TICKET_INVALID: "the sign-in expired before registration finished; run sock login again",
	sockv1.ErrorReason_ERROR_REASON_GROUP_FULL:                  "the group is full; ask for more members with sock limits request group-members <value> --reason <reason>",
	sockv1.ErrorReason_ERROR_REASON_LIMIT_REQUEST_PENDING:       "you already have a pending request for that limit; sock limits shows it",
}

// afterServerText covers the reasons whose server message carries the
// detail (which limit, the date to ask again): the text follows it.
var afterServerText = map[sockv1.ErrorReason]struct{ before, after string }{
	sockv1.ErrorReason_ERROR_REASON_LIMIT_REACHED: {"", "; erase a puppet you no longer use (sock puppets erase), revoke an unused token or machine key " +
		"(sock devices revoke), or ask for more puppets with sock limits request puppets <value> --reason <reason>"},
	sockv1.ErrorReason_ERROR_REASON_LIMIT_REQUEST_TOO_SOON: {"your last request for that limit was declined less than 30 days ago: ", ""},
}

// rewritten is an error with new text that still unwraps to the original,
// so Reason and Transient keep working.
type rewritten struct {
	text string
	err  error
}

func (r *rewritten) Error() string { return r.text }
func (r *rewritten) Unwrap() error { return r.err }

// describe words err for the person running sock. err has already been
// through client.Describe.
func (a *App) describe(err error) error {
	if err == nil {
		return nil
	}
	r := client.Reason(err)
	if text, ok := humanText[r]; ok && a.human() {
		return &rewritten{text, err}
	}
	if text, ok := reasonText[r]; ok {
		return &rewritten{text, err}
	}
	if text, ok := afterServerText[r]; ok {
		var ce *connect.Error
		if errors.As(err, &ce) && ce.Message() != "" {
			return &rewritten{text.before + ce.Message() + text.after, err}
		}
	}
	return err
}
