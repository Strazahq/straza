package main

import (
	"bytes"
	"debug/macho"
	"fmt"
)

// slices returns the thin images inside the universal binary data. Each
// fat_arch entry must name its slice's own architecture, no architecture may
// appear twice, and every byte outside the fat header, its fat_arch table and
// the slices must be zero, so that the slices are all the file holds.
func slices(path string, data []byte) ([]image, error) {
	ff, err := macho.NewFatFile(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s starts like a universal binary but its slices cannot be read (%v): check that the file is complete, and build it again with lipo if it is not", path, err)
	}
	rest := bytes.Clone(data)
	clear(rest[:8+20*len(ff.Arches)])
	seen := map[arch]bool{}
	var out []image
	for _, fa := range ff.Arches {
		end := uint64(fa.Offset) + uint64(fa.Size)
		if end > uint64(len(data)) {
			return nil, fmt.Errorf("%s starts like a universal binary but its slices cannot be read (a slice ends at byte %d of %d): check that the file is complete, and build it again with lipo if it is not", path, end, len(data))
		}
		im, err := thin(path, data[fa.Offset:end], true)
		if err != nil {
			return nil, err
		}
		if listed := (arch{fa.Cpu, fa.SubCpu &^ capabilityBits}); listed != im.arch {
			return nil, fmt.Errorf("%s lists a slice as %s in its universal header, but that slice's own header says %s, so the header does not describe the code a Mac would run: build the universal binary again with lipo from the thin builds and sign it again", path, listed, im.arch)
		}
		if seen[im.arch] {
			return nil, fmt.Errorf("%s holds two %s slices, so machocmp cannot prove which one a Mac runs: build the universal binary again with lipo from one thin build per architecture and sign it again", path, im.arch)
		}
		seen[im.arch] = true
		clear(rest[fa.Offset:end])
		out = append(out, im)
	}
	if off := len(rest) - len(bytes.TrimLeft(rest, "\x00")); off < len(rest) {
		return nil, fmt.Errorf("%s has a nonzero byte at offset %#x outside its universal header and its slices, so it carries data that is neither a build nor its code signature: build the universal binary again with lipo from the thin builds and sign it again", path, off)
	}
	return out, nil
}
