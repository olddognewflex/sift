// Command sift runs evidence-driven software engineering audits.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/olddognewflex/sift/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	// The first SIGINT/SIGTERM cancels the audit, which stops agents and
	// records partial results; a second one exits immediately.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	os.Exit(cli.Execute(ctx, cli.Deps{Stdout: os.Stdout, Stderr: os.Stderr, Version: version}, os.Args[1:]))
}
