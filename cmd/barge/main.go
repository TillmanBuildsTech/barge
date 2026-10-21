// Command barge deploys containerised applications to local or remote hosts.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/TillmanBuildsTech/barge/internal/app"
)

// version is stamped at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := app.New(version, app.DefaultRegistry()).Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
