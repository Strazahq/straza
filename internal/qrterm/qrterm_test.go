package qrterm

import (
	"strings"
	"testing"

	"rsc.io/qr"
)

// The realistic payloads: exactly what POST /v1/admin/approvers/enroll-token
// hands the CLI (internal/server/enrollqr.go, qrPayload): the compact form,
// and the widest one the contract allows (BYO-Firebase app config attached).
const (
	enrollPayload = `{"v":1,"servers":["https://192.168.1.42:8443"],"token":"wXTvB-hm3ltEub_uFdvqwl0yvyV_icGg7vzRvK2yy7M",` +
		`"pin":"sha256/V13tbotp+v8SUsHrXjWzV2jrpKRC5TZeBI/IShtoYSA=",` +
		`"project":{"id":"prj_0198f4c1-6a20-7c3e-b5d9-4f2a1c7e93b6","name":"straza-93b6"}}`
	enrollPayloadFCM = `{"v":1,"servers":["https://approver.enterprise.example.com:8443"],` +
		`"token":"wXTvB-hm3ltEub_uFdvqwl0yvyV_icGg7vzRvK2yy7M",` +
		`"pin":"sha256/V13tbotp+v8SUsHrXjWzV2jrpKRC5TZeBI/IShtoYSA=",` +
		`"project":{"id":"prj_0198f4c1-6a20-7c3e-b5d9-4f2a1c7e93b6","name":"acme-production"},` +
		`"fcm":{"project_id":"acme-straza-prod-4417","app_id":"1:928374651028:android:9f8e7d6c5b4a3210",` +
		`"api_key":"AIzaSyD-1234567890abcdefghijklmnopqrstuv","sender_id":"928374651028"}}`
)

var renderPayloads = []struct {
	name    string
	payload string
}{
	{"numeric", "1"},
	{"short url", "https://straza.dev"},
	{"enroll payload", enrollPayload},
	{"enroll payload with fcm", enrollPayloadFCM},
	{"non-ascii project name", `{"v":1,"project":{"name":"Ünïcode Ärbeit"}}`},
}

// TestRenderDrawsTheEncoderMatrix is the load-bearing test: a scanner reads the
// MODULES, so the render is read back into a module grid and compared cell by
// cell with the encoder's own matrix, plus the four-module quiet zone the spec
// requires on every side. Nothing else in this package can be trusted if this
// fails; we cannot hold a phone up to CI.
func TestRenderDrawsTheEncoderMatrix(t *testing.T) {
	for _, tc := range renderPayloads {
		t.Run(tc.name, func(t *testing.T) {
			sym, err := qr.Encode(tc.payload, qr.L)
			if err != nil {
				t.Fatalf("qr.Encode: %v", err)
			}
			code, err := Render(tc.payload)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			got := parseModules(t, code.Text)

			side := sym.Size + 2*QuietZone
			for y := 0; y < side; y++ {
				for x := 0; x < side; x++ {
					want := false
					if mx, my := x-QuietZone, y-QuietZone; mx >= 0 && my >= 0 && mx < sym.Size && my < sym.Size {
						want = sym.Black(mx, my)
					}
					if got[y][x] != want {
						t.Fatalf("module (%d,%d) = %v, want %v (quiet zone %d, symbol %d)",
							x, y, got[y][x], want, QuietZone, sym.Size)
					}
				}
			}
			// A symbol side is always odd, so the last text line carries half a
			// module row that is not part of the symbol: it must be light, or a
			// scanner sees a smear under the bottom quiet zone.
			for x := range got[side] {
				if got[side][x] {
					t.Fatalf("stray dark module at (%d,%d) below the symbol", x, side)
				}
			}
		})
	}
}

// TestRenderGeometry pins the reported shape against the drawn one. The
// caption tells the operator how wide the QR is, so a lie here sends them
// resizing the wrong window.
func TestRenderGeometry(t *testing.T) {
	for _, tc := range renderPayloads {
		t.Run(tc.name, func(t *testing.T) {
			sym, err := qr.Encode(tc.payload, qr.L)
			if err != nil {
				t.Fatalf("qr.Encode: %v", err)
			}
			code, err := Render(tc.payload)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if code.Modules != sym.Size {
				t.Errorf("Modules = %d, want %d", code.Modules, sym.Size)
			}
			if want := sym.Size + 2*QuietZone; code.Columns != want {
				t.Errorf("Columns = %d, want %d", code.Columns, want)
			}
			if want := (sym.Size + 2*QuietZone + 1) / 2; code.Lines != want {
				t.Errorf("Lines = %d, want %d", code.Lines, want)
			}

			lines := strings.Split(strings.TrimSuffix(code.Text, "\n"), "\n")
			if len(lines) != code.Lines {
				t.Fatalf("drew %d lines, reported %d", len(lines), code.Lines)
			}
			for i, line := range lines {
				if !strings.HasPrefix(line, sgrDarkOnLight) || !strings.HasSuffix(line, sgrReset) {
					t.Fatalf("line %d is not wrapped in the dark-on-light SGR pair: %q", i, line)
				}
				body := strings.TrimSuffix(strings.TrimPrefix(line, sgrDarkOnLight), sgrReset)
				if n := len([]rune(body)); n != code.Columns {
					t.Fatalf("line %d is %d columns, want %d", i, n, code.Columns)
				}
				for _, r := range body {
					switch r {
					case '█', '▀', '▄', ' ':
					default:
						t.Fatalf("line %d draws %q, which is not a half-block", i, r)
					}
				}
			}
		})
	}
}

// TestRenderColoursItsOwnBackground: a QR must be dark modules on a LIGHT
// background. Terminal themes are mostly dark, so the render sets both colours
// itself and resets at every line end; inheriting the theme would hand half
// the fleet an inverted code (which iOS's camera will not read) and leave a
// white background smeared across the next prompt.
func TestRenderColoursItsOwnBackground(t *testing.T) {
	code, err := Render(enrollPayload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := strings.Count(code.Text, sgrDarkOnLight); got != code.Lines {
		t.Errorf("%d colour openers for %d lines", got, code.Lines)
	}
	if got := strings.Count(code.Text, sgrReset+"\n"); got != code.Lines {
		t.Errorf("%d line-ending resets for %d lines", got, code.Lines)
	}
	if !strings.HasSuffix(code.Text, sgrReset+"\n") {
		t.Error("the block must end reset, not mid-colour")
	}
}

// TestRenderQuietZoneIsFourModules: the spec's margin, stated as its own test
// because it is the first thing an "it does not scan" report should rule out.
func TestRenderQuietZoneIsFourModules(t *testing.T) {
	if QuietZone != 4 {
		t.Fatalf("QuietZone = %d, want 4 (ISO/IEC 18004)", QuietZone)
	}
	code, err := Render(enrollPayload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	grid := parseModules(t, code.Text)
	side := code.Columns
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			inQuiet := x < QuietZone || y < QuietZone || x >= side-QuietZone || y >= side-QuietZone
			if inQuiet && grid[y][x] {
				t.Fatalf("dark module at (%d,%d) inside the quiet zone", x, y)
			}
		}
	}
}

// TestRenderIsByteStable pins one whole small render, escapes included. It is
// the tripwire for a silent change in the encoder, the colour pair or the
// glyphs: the matrix test proves we draw what the encoder produced, this one
// proves what the encoder produces has not moved under us.
func TestRenderIsByteStable(t *testing.T) {
	const payload = "STRAZA-QR"
	want := strings.Join([]string{
		"\x1b[30;47m                             \x1b[0m",
		"\x1b[30;47m                             \x1b[0m",
		"\x1b[30;47m    █▀▀▀▀▀█  █▄█▀ █▀▀▀▀▀█    \x1b[0m",
		"\x1b[30;47m    █ ███ █ ▀█ █▀ █ ███ █    \x1b[0m",
		"\x1b[30;47m    █ ▀▀▀ █   ▀ █ █ ▀▀▀ █    \x1b[0m",
		"\x1b[30;47m    ▀▀▀▀▀▀▀ █▄▀▄█ ▀▀▀▀▀▀▀    \x1b[0m",
		"\x1b[30;47m    ▀██ ▀█▀██▀▀▀ ▀█▄ ▄█      \x1b[0m",
		"\x1b[30;47m     █▄▄ ▀▀█ ▀▀ ▀ ▄▄▀██▄     \x1b[0m",
		"\x1b[30;47m        ▀▀▀▀▄  ▄▀▄▀▄██▀ ▀    \x1b[0m",
		"\x1b[30;47m    █▀▀▀▀▀█ █▄▄█▄█▀▀██▄█     \x1b[0m",
		"\x1b[30;47m    █ ███ █ ▀ ▀▀ ▀█ ▄ ██▀    \x1b[0m",
		"\x1b[30;47m    █ ▀▀▀ █ █▄▀ ▀ ▄██ ▀▄█    \x1b[0m",
		"\x1b[30;47m    ▀▀▀▀▀▀▀ ▀   ▀ ▀  ▀  ▀    \x1b[0m",
		"\x1b[30;47m                             \x1b[0m",
		"\x1b[30;47m                             \x1b[0m",
		"",
	}, "\n")

	code, err := Render(payload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if code.Text != want {
		t.Errorf("render moved:\ngot:\n%s\nwant:\n%s", code.Text, want)
	}
	if code.Modules != 21 || code.Columns != 29 || code.Lines != 15 {
		t.Errorf("geometry = %d modules / %d columns / %d lines, want 21/29/15",
			code.Modules, code.Columns, code.Lines)
	}
}

// TestRenderRefuses: the two inputs there is no QR for. Both return an error
// rather than an empty block, so the caller can say why nothing was drawn;
// the enroll token is already minted by then and must not be swallowed.
func TestRenderRefuses(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{"empty payload", "", "empty payload"},
		{"beyond QR capacity", strings.Repeat("x", 3000), "too long"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, err := Render(tc.payload)
			if err == nil {
				t.Fatalf("Render(%d bytes) = %q, want an error", len(tc.payload), code.Text)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
			if code != (Code{}) {
				t.Errorf("a failed Render returned %+v, want the zero Code", code)
			}
		})
	}
}

// parseModules reads a rendered block back into the module grid it draws: one
// bool per module, quiet zone included, two module rows per text line. It is
// the inverse of the renderer and deliberately simple, a scanner's-eye view.
func parseModules(t *testing.T, text string) [][]bool {
	t.Helper()
	var grid [][]bool
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		body, ok := strings.CutPrefix(line, sgrDarkOnLight)
		if !ok {
			t.Fatalf("line %d has no colour opener: %q", i, line)
		}
		body, ok = strings.CutSuffix(body, sgrReset)
		if !ok {
			t.Fatalf("line %d has no reset: %q", i, line)
		}
		var top, bottom []bool
		for _, r := range body {
			switch r {
			case '█':
				top, bottom = append(top, true), append(bottom, true)
			case '▀':
				top, bottom = append(top, true), append(bottom, false)
			case '▄':
				top, bottom = append(top, false), append(bottom, true)
			case ' ':
				top, bottom = append(top, false), append(bottom, false)
			default:
				t.Fatalf("line %d draws %q, which is not a half-block", i, r)
			}
		}
		grid = append(grid, top, bottom)
	}
	return grid
}
