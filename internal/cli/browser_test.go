package cli

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// sock-cli-mfj: an opener that starts and then fails (xdg-open with no
// display) must report the failure, so login prints the URL instead of
// "Opened your browser" and a wait that never ends.
func TestOpenWithReportsAnOpenerThatExitsNonZero(t *testing.T) {
	err := openWith(exec.Command("sh", "-c", "exit 3"), time.Second)
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("got %v, want the opener's exit status", err)
	}
}

func TestOpenWithAcceptsAnOpenerThatExitsZero(t *testing.T) {
	if err := openWith(exec.Command("true"), time.Second); err != nil {
		t.Fatal(err)
	}
}

// An opener that keeps running is the browser itself (xdg-open can exec
// it): that is a success, reported before the browser exits.
func TestOpenWithAcceptsAnOpenerThatKeepsRunning(t *testing.T) {
	start := time.Now()
	if err := openWith(exec.Command("sleep", "30"), 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("waited for the browser to exit")
	}
}

func TestOpenBrowserRefusesWithoutADisplay(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("display check is for xdg-open hosts")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	err := openBrowser("https://example.net/")
	if err == nil || !strings.Contains(err.Error(), "no display") {
		t.Fatalf("got %v, want a no display refusal", err)
	}
}
