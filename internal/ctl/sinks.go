package ctl

import (
	"context"
	"net/http"
	"net/url"
)

// SinkStreamInfo is one spine stream a sink drains (openapi.yaml listSinks).
type SinkStreamInfo struct {
	Stream      string `json:"stream"`
	Pending     uint64 `json:"pending"`
	Inflight    uint64 `json:"inflight"`
	Delivered   uint64 `json:"delivered"`
	Duplicates  uint64 `json:"duplicates"`
	Parked      uint64 `json:"parked"`
	LastError   string `json:"last_error"`
	LastErrorAt string `json:"last_error_at"`
}

// SinkInfo is the admin API view of one configured sink: target, filters,
// per-stream backlog and tallies, and the live dead-letter count.
type SinkInfo struct {
	Name     string           `json:"name"`
	Type     string           `json:"type"`
	Target   string           `json:"target"`
	Batch    int              `json:"batch"`
	Subjects []string         `json:"subjects"`
	Parked   uint64           `json:"parked"`
	Streams  []SinkStreamInfo `json:"streams"`
}

// Backlog is the sink's total not-yet-landed count across its streams
// (pending plus in-flight/awaiting-redelivery).
func (s SinkInfo) Backlog() uint64 {
	var n uint64
	for _, st := range s.Streams {
		n += st.Pending + st.Inflight
	}
	return n
}

// SinkReplayResult is one replay call's outcome (openapi.yaml replaySink).
type SinkReplayResult struct {
	Sink      string `json:"sink"`
	Replayed  int    `json:"replayed"`
	Remaining int    `json:"remaining"`
	Stopped   string `json:"stopped"`
}

// Sinks lists the configured sinks with their delivery state.
func (c *Client) Sinks(ctx context.Context) ([]SinkInfo, error) {
	var out []SinkInfo
	return out, c.Do(ctx, http.MethodGet, "/v1/admin/sinks", nil, &out)
}

// ReplaySink re-delivers a sink's parked events, up to limit (0 = server
// default, 1000).
func (c *Client) ReplaySink(ctx context.Context, name string, limit int) (SinkReplayResult, error) {
	var out SinkReplayResult
	var body any
	if limit > 0 {
		body = map[string]int{"limit": limit}
	}
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/sinks/"+url.PathEscape(name)+"/replay", body, &out)
}
