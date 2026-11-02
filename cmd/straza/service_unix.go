//go:build !windows

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/agentguard/service"
)

// runService runs the service in the foreground until SIGTERM or SIGINT,
// with its paths from STRAZA_HOME and its log on stderr.
func runService(parent context.Context) error {
	ctx, stop := signal.NotifyContext(parent, syscall.SIGTERM, os.Interrupt)
	defer stop()
	socket, stateDir, user := localapi.Paths(os.Getenv("STRAZA_HOME"))
	return service.Run(ctx, service.Options{
		Socket: socket, StateDir: stateDir, UserMode: user,
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
}
