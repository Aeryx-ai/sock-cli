// Command sock is the sock command line client.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/guygrigsby/sock-cli/internal/cli"
)

// version is set by -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.NewRoot(cli.DefaultApp(version)).ExecuteContext(ctx); err != nil {
		cli.Report(os.Stderr, err)
		os.Exit(1)
	}
}
