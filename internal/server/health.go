package server

import (
	"context"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/wire"
)

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleReadyz(components map[string]Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		out := wire.ReadyStatus{Status: "ok", Components: map[string]string{}}
		code := http.StatusOK
		for name, p := range components {
			if err := p.Ping(ctx); err != nil {
				out.Components[name] = err.Error()
				out.Status = "degraded"
				code = http.StatusServiceUnavailable
			} else {
				out.Components[name] = "ok"
			}
		}
		writeJSON(w, code, out)
	}
}
