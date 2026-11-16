package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/jrmash/gh-star-repos/internal/cli"
)

var version = "dev"

func main() {
	// Wait for Execute to finish canceling CLI processes before exiting on
	// either terminal interrupts or job termination (for example, timeout).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx, os.Args[1:], cli.Options{Version: version})
	stop()
	os.Exit(code)
}
