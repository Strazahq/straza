//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

// TestServiceRefusesAConsoleStart pins that `straza service` started outside
// the Service Control Manager, as this test binary is, refuses with the
// sentence that names the Windows service and how to start it.
func TestServiceRefusesAConsoleStart(t *testing.T) {
	err := runService(context.Background())
	if err == nil || err.Error() != consoleStart {
		t.Fatalf("runService from a console = %v, want %q", err, consoleStart)
	}
}

// TestSCMHandler pins the handler's side of the Service Control Manager
// contract: StartPending, Running that accepts Stop and Shutdown only once
// run calls ready, an echo for Interrogate, StopPending with the wait hint
// once run's context is cancelled, no Running for a ready that comes after
// a stop, and exit code 1 with a log line when run fails, before ready or
// after a stop, with no Running for a run that fails before ready.
func TestSCMHandler(t *testing.T) {
	errRefused := errors.New("the state folder is refused")
	readyUntilCancelled := func(err error) func(context.Context, func()) error {
		return func(ctx context.Context, ready func()) error { ready(); <-ctx.Done(); return err }
	}
	startPending := svc.Status{State: svc.StartPending}
	running := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	stopPending := svc.Status{State: svc.StopPending, WaitHint: stopWaitHint}
	tests := []struct {
		name     string
		run      func(context.Context, func()) error
		cmds     []svc.Cmd
		want     []svc.Status
		wantCode uint32
	}{
		{"stop", readyUntilCancelled(nil), []svc.Cmd{svc.Stop}, []svc.Status{startPending, running, stopPending}, 0},
		{"shutdown", readyUntilCancelled(nil), []svc.Cmd{svc.Shutdown}, []svc.Status{startPending, running, stopPending}, 0},
		{"interrogate, then stop", readyUntilCancelled(nil), []svc.Cmd{svc.Interrogate, svc.Stop},
			[]svc.Status{startPending, running, running, stopPending}, 0},
		{"run fails before ready", func(context.Context, func()) error { return errRefused }, nil,
			[]svc.Status{startPending}, 1},
		{"run fails after a stop", readyUntilCancelled(errRefused), []svc.Cmd{svc.Stop},
			[]svc.Status{startPending, running, stopPending}, 1},
		{"a ready after a stop is not reported", func(ctx context.Context, ready func()) error { <-ctx.Done(); ready(); return nil },
			[]svc.Cmd{svc.Stop}, []svc.Status{startPending, stopPending}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logged bytes.Buffer
			h := scmHandler{ctx: context.Background(), run: tt.run, log: slog.New(slog.NewTextHandler(&logged, nil))}
			r := make(chan svc.ChangeRequest)
			s := make(chan svc.Status, 8)
			type result struct {
				specific bool
				code     uint32
			}
			done := make(chan result, 1)
			go func() {
				specific, code := h.Execute(nil, r, s)
				done <- result{specific, code}
			}()
			var states []svc.Status
			next := func() {
				select {
				case st := <-s:
					states = append(states, st)
				case <-time.After(5 * time.Second):
					t.Fatalf("no status after %+v", states)
				}
			}
			// Commands go only after the statuses before them arrived, so a
			// stop never races the Running it follows.
			next()
			if slices.Contains(tt.want, running) {
				next()
			}
			for _, c := range tt.cmds {
				select {
				case r <- svc.ChangeRequest{Cmd: c, CurrentStatus: running}:
				case <-time.After(5 * time.Second):
					t.Fatalf("the handler did not take %v", c)
				}
			}
			var got result
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the handler did not return")
			}
			close(s)
			for st := range s {
				states = append(states, st)
			}
			if !slices.Equal(states, tt.want) {
				t.Errorf("statuses = %+v, want %+v", states, tt.want)
			}
			if got.code != tt.wantCode || got.specific != (tt.wantCode != 0) {
				t.Errorf("exit = %+v, want service-specific code %d", got, tt.wantCode)
			}
			if failed := strings.Contains(logged.String(), errRefused.Error()); failed != (tt.wantCode != 0) {
				t.Errorf("log %q, want the run error logged only when run failed", logged.String())
			}
		})
	}
}
