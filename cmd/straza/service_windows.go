//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"

	"github.com/strazahq/straza/internal/agentguard/localapi"
	"github.com/strazahq/straza/internal/agentguard/service"
)

// windowsServiceName names the Windows service and its event log source.
const windowsServiceName = "straza-service"

// consoleStart is the refusal of `straza service` started outside the
// Service Control Manager.
const consoleStart = "`straza service` runs as the Windows service straza-service and cannot run from a console. " +
	"Start it from an administrator PowerShell with `Start-Service straza-service`."

// stopWaitHint is how long the Service Control Manager is told a stop may
// take: the service's five-second shutdown with room to spare.
const stopWaitHint = 10_000 // milliseconds

// runService runs the service under the Service Control Manager, with the
// system pipe and state folder and its log in the Application event log.
// Started any other way, or when Windows cannot say how it started, it
// refuses and runs nothing.
func runService(ctx context.Context) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("`straza service` could not tell whether the Service Control Manager started it (%v), so it did not start. "+ //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
			"Start it from an administrator PowerShell with `Start-Service straza-service`.", err)
	}
	if !isService {
		return errors.New(consoleStart) //nolint:staticcheck // ST1005: the operator-facing sentence is printed as it is
	}
	log, closeLog := serviceLog()
	defer closeLog()
	socket, stateDir, _ := localapi.Paths("")
	run := func(ctx context.Context, ready func()) error {
		return service.Run(ctx, service.Options{Socket: socket, StateDir: stateDir, Log: log, Ready: ready})
	}
	if err := svc.Run(windowsServiceName, scmHandler{ctx: ctx, run: run, log: log}); err != nil {
		log.Error("the Service Control Manager did not run straza-service", "err", err)
		return err
	}
	return nil
}

// scmHandler runs the service for the Service Control Manager. run is
// service.Run with its options, and a test's fake. It calls ready once its
// pipe listens.
type scmHandler struct {
	ctx context.Context
	run func(ctx context.Context, ready func()) error
	log *slog.Logger
}

// Execute reports StartPending, and Running only once run calls ready, so a
// start that waits for the service sees it only when its pipe listens. It
// cancels run's context on Stop or Shutdown and reports StopPending while
// run returns, the way SIGTERM stops it on Linux, and a ready that comes
// after a stop is never reported. A run that fails, before ready or after
// a stop, logs its error and ends the service with the service-specific
// exit code 1, so the Service Control Manager records a failure and, with
// the service's failure flag set, runs its recovery actions.
func (h scmHandler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	listening := make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)
	go func() { done <- h.run(ctx, func() { once.Do(func() { close(listening) }) }) }()
	waitReady := listening
	for {
		select {
		case <-waitReady:
			waitReady = nil
			s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
		case err := <-done:
			if err != nil {
				h.log.Error("straza service stopped", "err", err)
				return true, 1
			}
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				cancel()
				waitReady = nil
				s <- svc.Status{State: svc.StopPending, WaitHint: stopWaitHint}
			}
		}
	}
}

// eventID is the event id of every record. The source carries no message
// table of its own, so one id serves every line.
const eventID = 1

// serviceLog returns the service's logger, which writes each record to the
// Application event log under the source straza-service, and a func that
// closes the log. When the event log cannot be opened it logs to stderr
// instead, because a logging failure never stops the service.
func serviceLog() (*slog.Logger, func()) {
	elog, err := eventlog.Open(windowsServiceName)
	if err != nil {
		return slog.New(slog.NewTextHandler(os.Stderr, nil)), func() {}
	}
	buf := new(bytes.Buffer)
	h := &eventLogHandler{mu: new(sync.Mutex), buf: buf, text: slog.NewTextHandler(buf, nil), elog: elog}
	return slog.New(h), func() { _ = elog.Close() }
}

// eventLogHandler formats each record as the text handler does and reports
// it to the event log as an Info, Warning or Error event by its level. A
// failed report is dropped by slog, so it never stops the service. Handlers
// made by WithAttrs and WithGroup share the buffer and its lock.
type eventLogHandler struct {
	mu   *sync.Mutex
	buf  *bytes.Buffer
	text slog.Handler
	elog *eventlog.Log
}

func (h *eventLogHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.text.Enabled(ctx, l)
}

func (h *eventLogHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.buf.Reset()
	if err := h.text.Handle(ctx, r); err != nil {
		return err
	}
	msg := strings.TrimSuffix(h.buf.String(), "\n")
	switch {
	case r.Level >= slog.LevelError:
		return h.elog.Error(eventID, msg)
	case r.Level >= slog.LevelWarn:
		return h.elog.Warning(eventID, msg)
	}
	return h.elog.Info(eventID, msg)
}

func (h *eventLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.text = h.text.WithAttrs(attrs)
	return &c
}

func (h *eventLogHandler) WithGroup(name string) slog.Handler {
	c := *h
	c.text = h.text.WithGroup(name)
	return &c
}
