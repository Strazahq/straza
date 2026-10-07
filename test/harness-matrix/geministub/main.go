// geministub is the gemini lane's local model endpoint: it lets a real
// gemini-cli run a COMPLETING turn with no API key by serving scripted model
// traffic behind GOOGLE_GEMINI_BASE_URL, unlocking the hook events a fake-key
// turn can never fire (AfterAgent, gemini's ONLY reply lane, and BeforeTool).
// Every request is logged to -log as JSONL before it is answered, so vendor
// wire drift shows up as data. The listen address goes to -addr-file; the
// bind is always 127.0.0.1:0, so parallel lanes cannot race a fixed port.
//
//   - *:streamGenerateContent (SSE): a functionResponse part or an empty -tool
//     answers -reply with finishReason STOP, else ONE functionCall for -tool.
//     Only the STREAM endpoint is scripted: a functionCall on the non-stream
//     router probe wedges the router and the turn hangs to timeout.
//   - *:generateContent (router lane): -reply, except that a generationConfig
//     carrying responseJsonSchema gets a minimal valid instance of that
//     schema. countTokens gets a fixed count, anything else 200 {}.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type logLine struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Query  string          `json:"query,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
}

func main() {
	addrFile := flag.String("addr-file", "", "write the bound host:port here")
	logFile := flag.String("log", "", "append every request as JSONL here")
	reply := flag.String("reply", "stub reply", "assistant text the scripted turn ends with")
	tool := flag.String("tool", "", "when set: first model turn is a functionCall for this tool")
	// Default matches list_directory's 0.53 signature (dir_path); a wrong
	// param name is rejected by the CLI's own validator BEFORE the BeforeTool
	// hook, so the tool gate would silently lose its event.
	toolArgs := flag.String("tool-args", `{"dir_path":"."}`, "functionCall args JSON for -tool")
	flag.Parse()

	var mu sync.Mutex
	logReq := func(r *http.Request, body []byte) {
		if *logFile == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer func() { _ = f.Close() }()
		raw := json.RawMessage(nil)
		if json.Valid(body) {
			raw = body
		}
		_ = json.NewEncoder(f).Encode(logLine{r.Method, r.URL.Path, r.URL.RawQuery, raw})
	}

	// candidate wraps parts as one model candidate in the REST wire shape.
	candidate := func(parts ...map[string]any) map[string]any {
		return map[string]any{
			"candidates": []any{map[string]any{
				"content":      map[string]any{"role": "model", "parts": parts},
				"finishReason": "STOP",
				"index":        0,
			}},
			"usageMetadata": map[string]any{"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2},
		}
	}

	// schemaInstance answers a responseJsonSchema request with a minimal
	// valid instance (gemini's uppercase type names: OBJECT/STRING/INTEGER…).
	var schemaInstance func(schema map[string]any) any
	schemaInstance = func(schema map[string]any) any {
		t, _ := schema["type"].(string)
		switch strings.ToUpper(t) {
		case "OBJECT":
			out := map[string]any{}
			props, _ := schema["properties"].(map[string]any)
			for name, p := range props {
				if ps, ok := p.(map[string]any); ok {
					out[name] = schemaInstance(ps)
				}
			}
			return out
		case "ARRAY":
			return []any{}
		case "INTEGER", "NUMBER":
			return 1
		case "BOOLEAN":
			return false
		default:
			return "stub"
		}
	}
	// responseSchema pulls generationConfig.responseJsonSchema from a request
	// body, when present.
	responseSchema := func(body []byte) map[string]any {
		var req struct {
			GenerationConfig struct {
				ResponseJSONSchema map[string]any `json:"responseJsonSchema"`
			} `json:"generationConfig"`
		}
		if json.Unmarshal(body, &req) != nil {
			return nil
		}
		return req.GenerationConfig.ResponseJSONSchema
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		logReq(r, body)
		switch {
		case strings.Contains(r.URL.Path, ":countTokens"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"totalTokens":7}`)
		case strings.Contains(r.URL.Path, ":streamGenerateContent"):
			var resp map[string]any
			// The tool round-trip marker: a functionResponse PART in the
			// request contents means the CLI already ran the tool we asked
			// for. Structural, not a substring probe: gemini 0.55's system
			// prompt narrates the literal word "functionResponse" (its Tool
			// Usage rules), so a raw body scan would misread turn 1 as
			// post-tool and starve the lane of its functionCall.
			afterTool := hasFunctionResponsePart(body)
			switch {
			case responseSchema(body) != nil:
				text, _ := json.Marshal(schemaInstance(responseSchema(body)))
				resp = candidate(map[string]any{"text": string(text)})
			case *tool != "" && !afterTool:
				args := json.RawMessage(*toolArgs)
				resp = candidate(map[string]any{"functionCall": map[string]any{"name": *tool, "args": args}})
			default:
				resp = candidate(map[string]any{"text": *reply})
			}
			out, _ := json.Marshal(resp)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\r\n\r\n", out)
		case strings.Contains(r.URL.Path, ":generateContent"):
			text := *reply
			if s := responseSchema(body); s != nil {
				b, _ := json.Marshal(schemaInstance(s))
				text = string(b)
			}
			out, _ := json.Marshal(candidate(map[string]any{"text": text}))
			w.Header().Set("Content-Type", "application/json")
			w.Write(out) //nolint:errcheck // best-effort stub
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{}`)
		}
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "geministub: listen: %v\n", err)
		os.Exit(1)
	}
	if *addrFile != "" {
		if err := os.WriteFile(*addrFile, []byte(ln.Addr().String()), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "geministub: addr-file: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Fprintf(os.Stderr, "geministub: serving on %s\n", ln.Addr())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	_ = srv.Serve(ln) //nolint:errcheck // killed by the lane
}

// hasFunctionResponsePart reports whether any contents[].parts[] entry carries
// a functionResponse FIELD, the structural mark of a completed tool round-trip.
// A parse failure answers false: the stub then offers its functionCall, and a
// broken request shape surfaces as visible lane behavior instead of a silently
// skipped tool turn.
func hasFunctionResponsePart(body []byte) bool {
	var req struct {
		Contents []struct {
			Parts []map[string]json.RawMessage `json:"parts"`
		} `json:"contents"`
	}
	if json.Unmarshal(body, &req) != nil {
		return false
	}
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if _, ok := p["functionResponse"]; ok {
				return true
			}
		}
	}
	return false
}
