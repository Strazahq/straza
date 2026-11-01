// Command machocmp proves that a signed macOS binary is the unsigned release
// build plus a code signature and nothing else. SIGNED is a thin or universal
// 64-bit little-endian Mach-O. Each UNSIGNED is a thin one, and each slice of
// SIGNED pairs with the one UNSIGNED of the same architecture. Both sides of
// a pair lose their code signature and get a __LINKEDIT vmsize equal to its
// filesize, and then their bytes must be equal. It prints one line per slice
// that matches and exits 0 when all do, exits 1 with a sentence per failure,
// and exits 2 with usage when it gets fewer than two arguments.
//
//	go run ./tools/machocmp SIGNED UNSIGNED...
package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	// lcCodeSignature is the LC_CODE_SIGNATURE load command.
	lcCodeSignature = 0x1d
	// capabilityBits are the cpusubtype bits that pairing ignores.
	capabilityBits = 0xff000000
	// headerSize is the size of a 64-bit Mach-O header.
	headerSize = 32
)

var le = binary.LittleEndian

// errUsage reports too few arguments.
var errUsage = errors.New("machocmp needs a signed binary and at least one unsigned binary")

const usage = `usage: go run ./tools/machocmp SIGNED UNSIGNED...

machocmp proves that each slice of SIGNED, a signed macOS binary, is one of
the UNSIGNED thin binaries plus a code signature and nothing else. Pass the
signed binary first, then the unsigned build of each architecture it holds.
`

func main() {
	err := run(os.Args[1:], os.Stdout)
	if errors.Is(err, errUsage) {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			fmt.Fprintln(os.Stderr, "machocmp:", line)
		}
		os.Exit(1)
	}
}

// run compares the slices of args[0] with the thin binaries in args[1:]. It
// writes one line to stdout per slice that matches and returns every failure
// joined, one sentence per line, or errUsage when args has fewer than two.
func run(args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return errUsage
	}
	signed, err := load(args[0], true)
	errs := []error{err}
	var unsigned []image
	for _, path := range args[1:] {
		imgs, err := load(path, false)
		errs = append(errs, err)
		unsigned = append(unsigned, imgs...)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	pairs, errs := pair(signed, unsigned)
	for _, p := range pairs {
		if err := compare(p[0], p[1]); err != nil {
			errs = append(errs, err)
			continue
		}
		fmt.Fprintf(stdout, "machocmp: %s is %s plus a code signature\n", p[0].name(), p[1].path)
	}
	return errors.Join(errs...)
}

// arch is a Mach-O cpu type and subtype, the subtype without its
// capability bits.
type arch struct {
	cpu macho.Cpu
	sub uint32
}

// String names a in Go's words for the two release platforms.
func (a arch) String() string {
	switch a {
	case arch{macho.CpuArm64, 0}:
		return "arm64"
	case arch{macho.CpuAmd64, 3}:
		return "amd64"
	}
	return fmt.Sprintf("cputype %#x subtype %#x", uint32(a.cpu), a.sub)
}

// image is one thin Mach-O image read from the file at path.
type image struct {
	path   string
	arch   arch
	data   []byte
	signed bool // it came from the SIGNED argument
}

// name is how messages call the image: a slice of the signed file, or the
// unsigned file itself.
func (im image) name() string {
	if im.signed {
		return fmt.Sprintf("the %s slice of %s", im.arch, im.path)
	}
	return im.path
}

// load reads the file at path and returns its thin images. A universal
// binary is accepted only when signed is true.
func load(path string, signed bool) ([]image, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the operator names the binaries to compare
	if err != nil {
		return nil, fmt.Errorf("cannot read %s, so it cannot be compared (%v): check the path and that this account may read the file", path, err)
	}
	if len(data) >= 4 && binary.BigEndian.Uint32(data) == macho.MagicFat {
		if !signed {
			return nil, fmt.Errorf("%s is a universal binary, but each unsigned input must be a thin one with one architecture: pass the go build output of each architecture instead", path)
		}
		return slices(path, data)
	}
	im, err := thin(path, data, signed)
	if err != nil {
		return nil, err
	}
	return []image{im}, nil
}

// thin checks that data is a 64-bit little-endian Mach-O image and returns
// it with its architecture.
func thin(path string, data []byte, signed bool) (image, error) {
	var magicLE, magicBE uint32
	if len(data) >= 4 {
		magicLE, magicBE = le.Uint32(data), binary.BigEndian.Uint32(data)
	}
	switch {
	case magicLE == macho.Magic64:
	case magicLE == macho.Magic32 || magicBE == macho.Magic32 || magicBE == macho.Magic64:
		return image{}, fmt.Errorf("%s is a 32-bit or big-endian Mach-O, and machocmp reads only the 64-bit little-endian kind that Go builds for darwin/arm64 and darwin/amd64: pass the straza binary from the package and the go build outputs for those two platforms", path)
	default:
		return image{}, fmt.Errorf("%s does not start with the header of a thin or universal Mach-O binary, so it holds no macOS code to compare: pass the straza binary from the package first and the go build outputs for darwin/arm64 and darwin/amd64 after it", path)
	}
	f, err := macho.NewFile(bytes.NewReader(data))
	if err != nil {
		return image{}, fmt.Errorf("%s has a Mach-O header but cannot be parsed (%v): check that the file is complete, and build it again if it is not", path, err)
	}
	return image{path: path, arch: arch{f.Cpu, f.SubCpu &^ capabilityBits}, data: data, signed: signed}, nil
}

// pair matches each signed slice with the one unsigned image of its
// architecture. It returns the pairs and an error for every slice without
// exactly one partner and every unsigned image without a slice.
func pair(signed, unsigned []image) ([][2]image, []error) {
	byArch := map[arch][]image{}
	for _, u := range unsigned {
		byArch[u.arch] = append(byArch[u.arch], u)
	}
	var pairs [][2]image
	var errs []error
	inSigned := map[arch]bool{}
	for _, s := range signed {
		inSigned[s.arch] = true
		switch us := byArch[s.arch]; len(us) {
		case 0:
			errs = append(errs, fmt.Errorf("%s has no unsigned binary of the same architecture among the inputs, so it cannot be checked: pass the unsigned build of that architecture too", s.name()))
		case 1:
			pairs = append(pairs, [2]image{s, us[0]})
		default:
			errs = append(errs, fmt.Errorf("%s and %s both hold the %s architecture, so machocmp cannot tell which one %s should equal: pass one unsigned binary per architecture", us[0].path, us[1].path, s.arch, s.name()))
		}
	}
	for _, u := range unsigned {
		if !inSigned[u.arch] {
			errs = append(errs, fmt.Errorf("%s holds the %s architecture, but %s has no %s slice, so either the wrong file was passed or the signed binary lacks that architecture: check the arguments, and if they are right, build the universal binary from every thin build and sign it again", u.path, u.arch, signed[0].path, u.arch))
		}
	}
	return pairs, errs
}

// compare normalizes both images and reports the first byte where they
// differ. The signed image must carry a code signature.
func compare(s, u image) error {
	ns, hasSig, err := normalize(s)
	if err != nil {
		return err
	}
	if !hasSig {
		return fmt.Errorf("%s carries no code signature, so it was not signed: sign the universal binary with codesign before comparing it", s.name())
	}
	nu, _, err := normalize(u)
	if err != nil {
		return err
	}
	if bytes.Equal(ns, nu) {
		return nil
	}
	off := 0
	for off < len(ns) && off < len(nu) && ns[off] == nu[off] {
		off++
	}
	return fmt.Errorf("%s differs from %s: with the code signatures removed, the signed slice has %d bytes and the unsigned binary %d, and they first differ at offset %#x, so the signed binary is not that build plus a code signature: rebuild both from the same commit with the same toolchain and flags, then sign again", s.name(), u.path, len(ns), len(nu), off)
}

// normalize returns a copy of im's bytes without its code signature and with
// the __LINKEDIT vmsize set to its filesize, and whether it had a signature.
// It follows StripDarwinSig in golang.org/x/build/cmd/gorebuild: the
// signature must be the last load command and its data must end both
// __LINKEDIT and the file. Its load command is zeroed and dropped from
// ncmds and sizeofcmds, __LINKEDIT ends where the signature started, and the
// file is cut there. The vmsize step is needed on both sides because
// codesign_allocate rounds the vmsize up when it adds a signature.
func normalize(im image) ([]byte, bool, error) {
	f, err := macho.NewFile(bytes.NewReader(im.data))
	if err != nil {
		return nil, false, fmt.Errorf("%s cannot be parsed as Mach-O (%v): check that the file is complete, and build it again if it is not", im.name(), err)
	}
	var link *macho.Segment
	linkAt, sigAt, off := 0, -1, headerSize
	for i, l := range f.Loads {
		raw := l.Raw()
		if seg, ok := l.(*macho.Segment); ok && seg.Name == "__LINKEDIT" {
			link, linkAt = seg, off
		}
		if le.Uint32(raw) == lcCodeSignature {
			if i != len(f.Loads)-1 {
				return nil, false, fmt.Errorf("%s has its code signature load command at position %d of %d instead of last, where codesign and the Go linker put it, so something changed the binary after it was signed: build it again from the release commit and sign it again", im.name(), i+1, len(f.Loads))
			}
			sigAt = off
		}
		off += len(raw)
	}
	if link == nil {
		return nil, false, fmt.Errorf("%s has no __LINKEDIT segment, so it is not a binary the Go linker wrote: pass the straza binary from the package and the go build outputs", im.name())
	}
	out := bytes.Clone(im.data)
	cut := uint64(len(out))
	if sigAt >= 0 {
		if cut, err = strip(out, f, link, linkAt, sigAt, im.name()); err != nil {
			return nil, false, err
		}
	}
	le.PutUint64(out[linkAt+32:], le.Uint64(out[linkAt+48:]))
	return out[:cut], sigAt >= 0, nil
}

// strip checks the code signature whose load command is at sigAt in out,
// takes it out of the header and the __LINKEDIT filesize, zeroes its load
// command, and returns the offset where the signature starts, at which the
// caller cuts the file.
func strip(out []byte, f *macho.File, link *macho.Segment, linkAt, sigAt int, name string) (uint64, error) {
	cmdsize := le.Uint32(out[sigAt+4:])
	if cmdsize != 16 {
		return 0, fmt.Errorf("%s has a code signature load command of %d bytes instead of 16, so it is malformed: build it again from the release commit and sign it again", name, cmdsize)
	}
	dataoff, size := uint64(le.Uint32(out[sigAt+8:])), uint64(le.Uint32(out[sigAt+12:]))
	end := link.Offset + link.Filesz
	if dataoff < link.Offset || dataoff+size != end || end != uint64(len(out)) {
		return 0, fmt.Errorf("%s has a code signature that does not end its __LINKEDIT segment and the file (the signature ends at byte %d, __LINKEDIT at %d and the file at %d), so it holds bytes that are neither the build nor the signature: build it again from the release commit and sign it again", name, dataoff+size, end, len(out))
	}
	le.PutUint32(out[16:], f.Ncmd-1)
	le.PutUint32(out[20:], f.Cmdsz-cmdsize)
	le.PutUint64(out[linkAt+48:], dataoff-link.Offset)
	clear(out[sigAt : sigAt+16])
	return dataoff, nil
}
