// Command sock is the sock command line client.
package main

import (
	"context"
	"fmt"
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
		fmt.Fprintln(os.Stderr, "sock:", err)
		os.Exit(1)
	}
}
