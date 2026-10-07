package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/redact"
	"github.com/strazahq/straza/internal/spine"
)

// The sinks admin surface (0.74.0): the operator door onto the dead-letter
// lane. The list says what each sink has behind it and what it refused,
// and the replay door re-feeds what parked once the receiver is healthy
// again. Config area: posture read,
// operational write (config:read / config:write), no request-path reads.

type sinkStreamPayload struct {
	Stream      string    `json:"stream"`
	Pending     uint64    `json:"pending"`
	Inflight    uint64    `json:"inflight"`
	Delivered   uint64    `json:"delivered"`
	Duplicates  uint64    `json:"duplicates"`
	Parked      uint64    `json:"parked"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitzero"`
}

type sinkPayload struct {
	Name     string              `json:"name"`
	Type     string              `json:"type"`
	Target   string              `json:"target"`
	Batch    int                 `json:"batch"`
	Subjects []string            `json:"subjects"`
	Parked   uint64              `json:"parked"`
	Streams  []sinkStreamPayload `json:"streams"`
}

// sinkTarget is the boot-announce redaction: scheme://host/path with the
// query masked (HEC-style receivers carry their token there); file sinks
// show their path.
func sinkTarget(sc config.Sink) string {
	if sc.Type == config.SinkFile {
		return sc.Path
	}
	return redact.URL(sc.URL)
}

// handleSinksList renders every configured sink with its per-stream runner
// state and the live parked count.
func (a *App) handleSinksList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := make([]sinkPayload, 0, len(a.cfg.Sinks))
	for _, sc := range a.cfg.Sinks {
		p := sinkPayload{Name: sc.Name, Type: sc.Type, Target: sinkTarget(sc), Batch: sc.Batch,
			Streams: []sinkStreamPayload{}}
		for _, runner := range a.sinks {
			if runner.SinkName() != sc.Name {
				continue
			}
			if p.Subjects == nil {
				p.Subjects = runner.Filters()
			} else {
				p.Subjects = append(p.Subjects, runner.Filters()...)
			}
			st := runner.Stats()
			row := sinkStreamPayload{Stream: runner.Stream(), Delivered: st.Delivered,
				Duplicates: st.Duplicates, Parked: st.Parked, LastError: st.LastError, LastErrorAt: st.LastErrorAt}
			// A runner that has not built its consumer yet reports zero
			// backlog rather than failing the whole list.
			if pending, inflight, err := runner.Backlog(ctx); err == nil {
				row.Pending, row.Inflight = pending, inflight
			}
			p.Streams = append(p.Streams, row)
		}
		if p.Subjects == nil {
			p.Subjects = []string{}
		}
		if n, err := a.bus.DeadLetterCount(ctx, spine.DeadLetterKey(sc.Name)); err == nil {
			p.Parked = n
		} else {
			a.log.Warn("sinks list: dead-letter count", "sink", sc.Name, "err", err)
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSinkReplay re-delivers a sink's parked events (oldest first, up to
// limit per call, default and cap 1000) and reports what it moved and what
// remains. Stops at the first refusal and says so; nothing is re-parked.
func (a *App) handleSinkReplay(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var target spine.Sink
	for _, runner := range a.sinks {
		if runner.SinkName() == name {
			target = runner.Target()
			break
		}
	}
	if target == nil {
		apiError(w, http.StatusNotFound, "no such sink: sinks are named in strazad config (sinks[].name)")
		return
	}
	var req struct {
		Limit int `json:"limit"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			apiError(w, http.StatusBadRequest, "body must be JSON: {\"limit\": N} (optional)")
			return
		}
	}
	res, err := spine.ReplaySink(r.Context(), a.bus, target, req.Limit, a.metrics)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "replay failed; see strazad log", fmt.Errorf("sink %s: %w", name, err))
		return
	}
	// Rare and operator-driven: one Info line with the tallies, keyed by
	// the request's correlation id, like every state-transition line.
	a.reqlog(r.Context()).Info("sink replay", "component", "sink", "sink", name,
		"replayed", res.Replayed, "remaining", res.Remaining, "stopped", res.Stopped)
	a.emitEvent(r, "straza.audit.admin", map[string]any{"action": "sink.replay", "sink": name,
		"replayed": res.Replayed, "remaining": res.Remaining, "stopped": res.Stopped})
	writeJSON(w, http.StatusOK, map[string]any{"sink": name, "replayed": res.Replayed,
		"remaining": res.Remaining, "stopped": res.Stopped})
}
