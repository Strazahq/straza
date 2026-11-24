package service

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// hostMax is the most bytes of a host name a check-in carries, the
// server's bound on the check-in's host.
const hostMax = 256

// hostName returns the host name a read of os.Hostname gave, as a check-in
// carries it: empty when the read failed, or when the name holds a NUL
// character or is not valid UTF-8, which the server refuses, and otherwise
// cut to hostMax bytes on a rune boundary, keeping its start.
func hostName(name string, err error) string {
	if err != nil || strings.ContainsRune(name, 0) || !utf8.ValidString(name) {
		return ""
	}
	if len(name) <= hostMax {
		return name
	}
	cut := hostMax
	for !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut]
}

// Start returns c's session for h as Session does with start set. A
// session it checks in keeps folder, and a live session it reuses keeps
// its own.
func (s *credStore) Start(ctx context.Context, c Caller, h localapi.Harness, folder string) (Session, error) {
	return s.session(ctx, c, h, true, folder)
}

// startFolder returns the folder a start for the window of hs checks in
// with: folder, or when folder is empty the folder hs keeps, so a revoked
// or lapsed window that the proxy lane starts again, naming no folder,
// keeps its own. With no held window it is folder.
func (a *account) startFolder(hs *heldSession, folder string) string {
	if folder != "" || hs == nil {
		return folder
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return hs.s.Folder
}
