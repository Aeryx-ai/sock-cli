// Package cli defines the sock command tree.
package cli

import "github.com/spf13/cobra"

// App holds everything commands depend on, so tests can substitute any of it.
type App struct {
	Version string
}

// DefaultApp wires the real environment.
func DefaultApp(version string) *App {
	return &App{Version: version}
}

// NewRoot builds the command tree.
func NewRoot(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:           "sock",
		Short:         "Message your owner and conversations from scripts, jobs and agents",
		Version:       app.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	return root
}
