//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	client "github.com/aeryx-ai/sock-client"

	"github.com/aeryx-ai/sock-cli/internal/socktest"
)

// security review 2: an ensure that holds the project's cache lock does not
// make the next one wait forever
func TestPuppetsEnsureGivesUpOnABusyCache(t *testing.T) {
	srv := socktest.New(t)
	app, _ := ensureApp(t, srv, socktest.MachineKeyToken)
	app.EnsureTimeout = 300 * time.Millisecond
	dir := filepath.Join(t.TempDir(), "myproj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := client.Projects{Dir: client.ProjectsDir(app.Getenv, client.Config{URL: srv.URL, Token: socktest.MachineKeyToken})}
	path, err := p.TokenFile(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	err = ensureWithin(t, app, dir, 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("err %v", err)
	}
}
