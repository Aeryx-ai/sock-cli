package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// run executes the root command with args and returns what it printed.
func run(t *testing.T, app *App, stdin io.Reader, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRoot(app)
	root.SetArgs(args)
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	root.SetIn(stdin)
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestVersionFlag(t *testing.T) {
	out, _, err := run(t, DefaultApp("1.2.3"), nil, "--version")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "1.2.3") {
		t.Fatalf("version output %q lacks 1.2.3", out)
	}
}
