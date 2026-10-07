// Package qrterm draws a QR symbol into a terminal, so an operator on a
// headless box can enroll a phone without a browser.
//
// The whole package is one idea: take the module matrix an encoder produced
// and paint it with the Unicode half-blocks ▀ ▄ █, two module rows per text
// line, which keeps a module roughly square in a terminal cell and halves the
// vertical space a QR needs. Nothing here decides WHAT to encode.
//
// Three properties make or break whether a phone can read the result, and each
// has a test: the drawn modules are exactly the encoder's modules; a
// four-module quiet zone surrounds the symbol (ISO/IEC 18004 §6.3.8); and the
// block paints its own colours, dark on light, because terminal themes are
// mostly light-on-dark and an inverted QR is one many cameras will not read.
package qrterm

import (
	"errors"
	"fmt"
	"strings"

	"rsc.io/qr"
)

// QuietZone is the light margin around the symbol, in modules. Four is the
// spec's minimum for a QR Code symbol and the reason a QR surrounded by
// terminal output still scans.
const QuietZone = 4

// The colour pair every line opens with and the reset every line ends with:
// black foreground on a white background, set explicitly because the symbol
// must be dark-on-light whatever the operator's theme is, and reset per line so
// a truncated or copied line cannot leave the terminal painted white.
const (
	sgrDarkOnLight = "\x1b[30;47m"
	sgrReset       = "\x1b[0m"
)

// Code is one rendered QR: the block to write, plus the geometry a caller may
// want to tell the operator about (a QR wider than the window wraps, and a
// wrapped QR does not scan).
type Code struct {
	Text    string // the block, one newline-terminated line per pair of module rows
	Modules int    // symbol side in modules, quiet zone excluded
	Columns int    // terminal columns the block occupies (Modules + 2*QuietZone)
	Lines   int    // terminal lines the block occupies
}

// Render encodes payload as a QR symbol and draws it for a terminal.
//
// Error correction is level L. The symbol is read off a clean screen by a
// camera held a foot away (there is no print damage, no dirt and no fading to
// recover from), so redundancy buys nothing here, while every level up widens
// the block: the enroll payload with a Firebase app config attached is 85
// columns at L and 93 at M, and a QR that does not fit the window cannot be
// scanned at all. (libqrencode, the tool operators reach for otherwise,
// defaults to L for the same reason.)
func Render(payload string) (Code, error) {
	if payload == "" {
		return Code{}, errors.New("qrterm: empty payload, nothing to encode")
	}
	sym, err := qr.Encode(payload, qr.L)
	if err != nil {
		return Code{}, fmt.Errorf("qrterm: %w (%d bytes)", err, len(payload))
	}
	return draw(sym.Black, sym.Size), nil
}

// draw paints a size×size module matrix, padded with the quiet zone, as
// half-blocks. It reads the matrix through dark() and knows nothing else about
// QR: whatever the encoder decided is what lands on the screen.
func draw(dark func(x, y int) bool, size int) Code {
	side := size + 2*QuietZone
	// Outside the symbol (the quiet zone, and the half module row below an
	// odd-sided symbol) is light.
	module := func(x, y int) bool {
		mx, my := x-QuietZone, y-QuietZone
		if mx < 0 || my < 0 || mx >= size || my >= size {
			return false
		}
		return dark(mx, my)
	}

	lines := (side + 1) / 2
	var b strings.Builder
	b.Grow(lines * (side*3 + len(sgrDarkOnLight) + len(sgrReset) + 1))
	for y := 0; y < side; y += 2 {
		b.WriteString(sgrDarkOnLight)
		for x := 0; x < side; x++ {
			top, bottom := module(x, y), y+1 < side && module(x, y+1)
			switch {
			case top && bottom:
				b.WriteRune('█')
			case top:
				b.WriteRune('▀')
			case bottom:
				b.WriteRune('▄')
			default:
				b.WriteRune(' ')
			}
		}
		b.WriteString(sgrReset)
		b.WriteByte('\n')
	}
	return Code{Text: b.String(), Modules: size, Columns: side, Lines: lines}
}
