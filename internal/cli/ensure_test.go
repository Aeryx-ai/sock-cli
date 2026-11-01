package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	client "github.com/aeryx-ai/sock-client"

	"github.com/aeryx-ai/sock-cli/internal/socktest"
)

// security review 2: a hub that accepts and never answers does not hang
// ensure, which holds the project's cache lock while it runs
func TestPuppetsEnsureGivesUpOnAHubThatNeverAnswers(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // so the server notices the client leave
		<-r.Context().Done()
	}))
	t.Cleanup(hang.Close)
	t.Cleanup(hang.CloseClientConnections)
	state := filepath.Join(t.TempDir(), "state")
	vars := map[string]string{"SOCK_URL": hang.URL, "SOCK_TOKEN": socktest.MachineKeyToken, "XDG_STATE_HOME": state, "HOME": t.TempDir()}
	app := testApp(t, nil)
	app.Getenv = func(k string) string { return vars[k] }
	app.EnsureTimeout = 300 * time.Millisecond
	dir := filepath.Join(t.TempDir(), "myproj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// no cached token: making the puppet gives up
	if err := ensureWithin(t, app, dir, 10*time.Second); err == nil || !strings.Contains(err.Error(), "gave up after") {
		t.Fatalf("err %v", err)
	}
	// a cached token the hub never answers about is kept, not waited on
	p := client.Projects{Dir: client.ProjectsDir(app.Getenv, client.Config{URL: hang.URL, Token: socktest.MachineKeyToken})}
	path, err := p.TokenFile(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte("sock_pt_cached_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureWithin(t, app, dir, 10*time.Second); err != nil {
		t.Fatalf("err %v", err)
	}
}

// ensureWithin runs puppets ensure and fails the test if it outlasts limit.
func ensureWithin(t *testing.T, app *App, dir string, limit time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, _, err := run(t, app, nil, "puppets", "ensure", dir)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("puppets ensure still running after %s", limit)
		return nil
	}
}
