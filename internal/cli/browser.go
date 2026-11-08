package cli

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// openerPatience is how long openBrowser waits for the opener to fail.
// xdg-open with no display or no browser exits non-zero well within it;
// an opener still running after it is the browser itself.
const openerPatience = 2 * time.Second

// openBrowser opens url with the platform's opener. The URL is an argument,
// never shell text. On an xdg-open host with no display it refuses at
// once, so login prints the URL instead of waiting for a browser that
// never comes (sock-cli-mfj).
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no display")
		}
		cmd = exec.Command("xdg-open", url)
	}
	return openWith(cmd, openerPatience)
}

// openWith starts cmd and reports its failure if it exits non-zero within
// patience. An opener that exits zero, or is still running then, opened
// the browser.
func openWith(cmd *exec.Cmd, patience time.Duration) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(patience):
		return nil
	}
}
