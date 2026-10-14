package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// A prompt-injected --exec child must not be able to read sock's credentials.
func TestRunShellKeepsSecretsFromTheChild(t *testing.T) {
	t.Setenv("SOCK_TOKEN", "sock_mk_parent_secret")
	t.Setenv("Sock_Access_Gate", "gate_secret")
	t.Setenv("CLAUDE_PLUGIN_OPTION_TOKEN", "sock_st_plugin_secret")
	t.Setenv("GIT_DIR", "/kept")
	var out, errOut bytes.Buffer
	if err := runShell(context.Background(), "env", nil, []string{"SOCK_MESSAGE_ID=m1"}, &out, &errOut); err != nil {
		t.Fatalf("env: %v %s", err, errOut.String())
	}
	got := out.String()
	for _, secret := range []string{"sock_mk_parent_secret", "gate_secret", "sock_st_plugin_secret"} {
		if strings.Contains(got, secret) {
			t.Errorf("child saw %s", secret)
		}
	}
	if !strings.Contains(got, "GIT_DIR=/kept") || !strings.Contains(got, "SOCK_MESSAGE_ID=m1") {
		t.Errorf("child lost what it needs:\n%s", got)
	}
}
