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
	sockv1.ErrorReason_ERROR_REASON_APPLE_TOKEN_INVALID: "Apple's sign-in was refused or has expired; run the command again",
	sockv1.ErrorReason_ERROR_REASON_NOT_REGISTERED:      "no sock account for this Apple ID; run sock register with an invite",
	sockv1.ErrorReason_ERROR_REASON_ALREADY_REGISTERED:  "this Apple ID already has a sock account; run sock login",
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
