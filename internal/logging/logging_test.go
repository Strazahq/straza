package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
)

func TestJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	log := New(config.Log{Level: "info", Format: "json"}, &buf)
	log.Info("hello", "k", "v")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("output is not JSON: %v: %s", err, buf.String())
	}
	if rec["msg"] != "hello" || rec["k"] != "v" {
		t.Errorf("unexpected record: %v", rec)
	}
}

func TestTextFormat(t *testing.T) {
	var buf bytes.Buffer
	log := New(config.Log{Level: "info", Format: "text"}, &buf)
	log.Info("hello")
	if !strings.Contains(buf.String(), "msg=hello") {
		t.Errorf("unexpected text output: %s", buf.String())
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log := New(config.Log{Level: "error", Format: "json"}, &buf)
	log.Info("dropped")
	if buf.Len() != 0 {
		t.Errorf("info record should be filtered at error level: %s", buf.String())
	}
	log.Error("kept")
	if buf.Len() == 0 {
		t.Error("error record missing")
	}
}
