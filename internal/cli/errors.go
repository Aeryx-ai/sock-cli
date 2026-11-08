package cli

import (
	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"

	client "github.com/aeryx-ai/sock-client"
)

const (
	humanTokenPrefix  = "sock_st_"
	puppetTokenPrefix = "sock_pt_"
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
	return err
}
