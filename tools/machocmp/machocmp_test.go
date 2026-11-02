package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The synthesized images are laid out the way the Go linker lays out a
// binary: the header and load commands, zero header padding, the __TEXT
// contents up to linkOff, then __LINKEDIT, which ends with the code
// signature when the image has one.
const (
	textStart = 0x200
	linkOff   = 0x1000
	linkData  = 0x100
	pageSize  = 0x4000
	lib64     = 0x80000000
)

func put32(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
func put64(b []byte, off int, v uint64) { binary.LittleEndian.PutUint64(b[off:], v) }

// segment writes a 64-bit segment load command with no sections at off.
func segment(b []byte, off int, name string, vmaddr, fileoff, filesize uint64) {
	put32(b, off, uint32(macho.LoadCmdSegment64))
	put32(b, off+4, 72)
	copy(b[off+8:off+24], name)
	put64(b, off+24, vmaddr)
	put64(b, off+32, filesize)
	put64(b, off+40, fileoff)
	put64(b, off+48, filesize)
	put32(b, off+56, 1)
	put32(b, off+60, 1)
}

// synth returns a thin 64-bit Mach-O image of the given cpu type and
// subtype. A positive sigSize ends __LINKEDIT with a code signature of that
// many bytes and adds LC_CODE_SIGNATURE as the last load command, as the Go
// linker does for darwin/arm64. The __LINKEDIT vmsize equals its filesize.
func synth(cpu macho.Cpu, sub uint32, sigSize int) []byte {
	ncmds, cmdsz := uint32(2), uint32(2*72)
	if sigSize > 0 {
		ncmds, cmdsz = ncmds+1, cmdsz+16
	}
	b := make([]byte, linkOff+linkData+sigSize)
	put32(b, 0, macho.Magic64)
	put32(b, 4, uint32(cpu))
	put32(b, 8, sub)
	put32(b, 12, uint32(macho.TypeExec))
	put32(b, 16, ncmds)
	put32(b, 20, cmdsz)
	segment(b, 32, "__TEXT", 0x100000000, 0, linkOff)
	segment(b, 104, "__LINKEDIT", 0x100000000+linkOff, linkOff, uint64(linkData+sigSize))
	if sigSize > 0 {
		put32(b, 176, lcCodeSignature)
		put32(b, 180, 16)
		put32(b, 184, linkOff+linkData)
		put32(b, 188, uint32(sigSize))
	}
	for i := textStart; i < len(b); i++ {
		b[i] = byte(i*7 + i>>8)
	}
	return b
}

// resign returns a copy of img as codesign_allocate leaves it when codesign
// adds or replaces a code signature of sigSize bytes. The new signature takes
// the place of the old one, or a new last load command in the header padding
// and the end of __LINKEDIT, and the __LINKEDIT vmsize is rounded up to 16 KiB.
func resign(t *testing.T, img []byte, sigSize int) []byte {
	t.Helper()
	f, err := macho.NewFile(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	out := bytes.Clone(img)
	off, linkAt := 32, 0
	var link *macho.Segment
	for _, l := range f.Loads {
		if s, ok := l.(*macho.Segment); ok && s.Name == "__LINKEDIT" {
			link, linkAt = s, off
		}
		off += len(l.Raw())
	}
	last := f.Loads[len(f.Loads)-1].Raw()
	sigAt, dataoff := off-16, link.Offset+link.Filesz
	if binary.LittleEndian.Uint32(last) == lcCodeSignature {
		dataoff = uint64(binary.LittleEndian.Uint32(last[8:]))
	} else {
		sigAt = off
		if !bytes.Equal(out[sigAt:sigAt+16], make([]byte, 16)) {
			t.Fatal("no zero header padding for a new load command")
		}
		put32(out, 16, f.Ncmd+1)
		put32(out, 20, f.Cmdsz+16)
		put32(out, sigAt, lcCodeSignature)
		put32(out, sigAt+4, 16)
	}
	if dataoff%16 != 0 {
		t.Fatalf("__LINKEDIT ends at %#x, which is not 16-byte aligned", dataoff)
	}
	put32(out, sigAt+8, uint32(dataoff))
	put32(out, sigAt+12, uint32(sigSize))
	out = append(out[:dataoff], bytes.Repeat([]byte{0xc5}, sigSize)...)
	size := dataoff - link.Offset + uint64(sigSize)
	put64(out, linkAt+32, (size+pageSize-1)/pageSize*pageSize)
	put64(out, linkAt+48, size)
	return out
}

// fat returns a universal binary that holds the given thin images, each at
// an offset aligned to 16 KiB, as lipo lays them out.
func fat(slices ...[]byte) []byte {
	be := binary.BigEndian
	out := make([]byte, pageSize)
	be.PutUint32(out, macho.MagicFat)
	be.PutUint32(out[4:], uint32(len(slices)))
	for i, s := range slices {
		e := out[8+20*i:]
		be.PutUint32(e, binary.LittleEndian.Uint32(s[4:]))
		be.PutUint32(e[4:], binary.LittleEndian.Uint32(s[8:]))
		be.PutUint32(e[8:], uint32(len(out)))
		be.PutUint32(e[12:], uint32(len(s)))
		be.PutUint32(e[16:], 14)
		out = append(out, s...)
		out = append(out, make([]byte, (pageSize-len(out)%pageSize)%pageSize)...)
	}
	return out
}

// label returns a copy of the universal binary f whose fat_arch entry i
// names the given cpu type and subtype.
func label(f []byte, i int, cpu macho.Cpu, sub uint32) []byte {
	out := bytes.Clone(f)
	binary.BigEndian.PutUint32(out[8+20*i:], uint32(cpu))
	binary.BigEndian.PutUint32(out[12+20*i:], sub)
	return out
}

// flip returns a copy of img with the byte at off inverted.
func flip(img []byte, off uint64) []byte {
	out := bytes.Clone(img)
	out[off] ^= 0xff
	return out
}

// sigFirst returns a copy of a synth image with a signature whose
// LC_CODE_SIGNATURE comes before the __LINKEDIT segment command.
func sigFirst(img []byte) []byte {
	out := bytes.Clone(img)
	copy(out[104:], img[176:192])
	copy(out[120:], img[104:176])
	return out
}

func TestRun(t *testing.T) {
	amd := synth(macho.CpuAmd64, 3|lib64, 0)
	arm := synth(macho.CpuArm64, 0, 0x110)
	amdSigned := resign(t, amd, 0x2400)
	armSigned := resign(t, arm, 0x2400)
	shortSig := bytes.Clone(armSigned)
	put32(shortSig, 188, 0x2400-16)
	padded := fat(armSigned, amdSigned)
	padded[pageSize+len(armSigned)] = 'P'
	ok := func(arch, u string) string {
		return "machocmp: the " + arch + " slice of {s} is " + u + " plus a code signature"
	}
	tests := []struct {
		name     string
		signed   []byte
		unsigned [][]byte
		want     []string // stdout lines, with {s} and {uN} standing for the paths
		wantErr  []string // substrings of the error, or nil when the run passes
	}{
		{"amd64 with a signature added", amdSigned, [][]byte{amd}, []string{ok("amd64", "{u0}")}, nil},
		{"arm64 re-signed with a bigger blob", armSigned, [][]byte{arm}, []string{ok("arm64", "{u0}")}, nil},
		{"universal with both partners", fat(armSigned, amdSigned), [][]byte{amd, arm},
			[]string{ok("arm64", "{u1}"), ok("amd64", "{u0}")}, nil},
		{"flipped byte in __TEXT", flip(armSigned, textStart+5), [][]byte{arm}, nil,
			[]string{"the arm64 slice of {s} differs from {u0}", "at offset 0x205", "rebuild both from the same commit with the same toolchain and flags, then sign again"}},
		{"flipped byte in __LINKEDIT before the signature", flip(amdSigned, linkOff+3), [][]byte{amd}, nil,
			[]string{"the amd64 slice of {s} differs from {u0}", "at offset 0x1003"}},
		{"capability bits ignored for pairing", amdSigned, [][]byte{synth(macho.CpuAmd64, 3, 0)}, nil,
			[]string{"differs from {u0}", "at offset 0xb"}},
		{"signature command not last", sigFirst(armSigned), [][]byte{arm}, nil,
			[]string{"the arm64 slice of {s} has its code signature load command at position 2 of 3"}},
		{"signature does not end __LINKEDIT", shortSig, [][]byte{arm}, nil,
			[]string{"the arm64 slice of {s} has a code signature that does not end"}},
		{"bytes after the signature", append(bytes.Clone(armSigned), 0xee), [][]byte{arm}, nil,
			[]string{"the arm64 slice of {s} has a code signature that does not end its __LINKEDIT segment and the file"}},
		{"signed slice without a signature", amd, [][]byte{amd}, nil,
			[]string{"the amd64 slice of {s} carries no code signature"}},
		{"universal missing one partner", fat(armSigned, amdSigned), [][]byte{arm}, []string{ok("arm64", "{u0}")},
			[]string{"the amd64 slice of {s} has no unsigned binary of the same architecture"}},
		{"extra unsigned", armSigned, [][]byte{arm, amd}, []string{ok("arm64", "{u0}")},
			[]string{"{u1} holds the amd64 architecture, but {s} has no amd64 slice"}},
		{"two partners for one slice", armSigned, [][]byte{arm, arm}, nil,
			[]string{"{u0} and {u1} both hold the arm64 architecture"}},
		{"universal unsigned", armSigned, [][]byte{fat(arm)}, nil,
			[]string{"{u0} is a universal binary, but each unsigned input must be a thin one"}},
		{"not Mach-O", []byte("#!/bin/sh\necho hi\n"), [][]byte{arm}, nil,
			[]string{"{s} does not start with the header of a thin or universal Mach-O binary"}},
		{"32-bit header", []byte{0xce, 0xfa, 0xed, 0xfe, 7, 0, 0, 0}, [][]byte{arm}, nil,
			[]string{"{s} is a 32-bit or big-endian Mach-O"}},
		{"big-endian header", armSigned, [][]byte{{0xfe, 0xed, 0xfa, 0xcf, 1, 0, 0, 0x12}}, nil,
			[]string{"{u0} is a 32-bit or big-endian Mach-O"}},
		{"truncated universal", fat(armSigned, amdSigned)[:pageSize+0x800], [][]byte{arm, amd}, nil,
			[]string{"{s} starts like a universal binary but its slices cannot be read"}},
		{"bytes after the last slice", append(fat(armSigned, amdSigned), "#!/bin/sh\n"...), [][]byte{arm, amd}, nil,
			[]string{"{s} has a nonzero byte at offset 0xc000 outside its universal header and its slices"}},
		{"bytes in the padding between slices", padded, [][]byte{arm, amd}, nil,
			[]string{"{s} has a nonzero byte at offset 0x7500 outside its universal header and its slices"}},
		{"fat label that its slice contradicts", label(fat(armSigned, amdSigned, armSigned), 2, macho.CpuAmd64, 8), [][]byte{arm, amd}, nil,
			[]string{"{s} lists a slice as cputype 0x1000007 subtype 0x8 in its universal header, but that slice's own header says arm64"}},
		{"two slices of one architecture", label(fat(armSigned, amdSigned, armSigned), 2, macho.CpuArm64, lib64), [][]byte{arm, amd}, nil,
			[]string{"{s} holds two arm64 slices"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			args := []string{filepath.Join(dir, "signed")}
			pairs := []string{"{s}", args[0]}
			for i, u := range tc.unsigned {
				p := filepath.Join(dir, "unsigned"+strconv.Itoa(i))
				args = append(args, p)
				pairs = append(pairs, "{u"+strconv.Itoa(i)+"}", p)
				writeFile(t, p, u)
			}
			writeFile(t, args[0], tc.signed)
			subst := strings.NewReplacer(pairs...)
			var stdout bytes.Buffer
			err := run(args, &stdout)
			var want string
			for _, l := range tc.want {
				want += subst.Replace(l) + "\n"
			}
			if stdout.String() != want {
				t.Errorf("stdout:\n%s\nwant:\n%s", stdout.String(), want)
			}
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("run: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("run passed, want a failure")
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(err.Error(), subst.Replace(w)) {
					t.Errorf("error %q\nlacks %q", err, subst.Replace(w))
				}
			}
		})
	}
}

func TestRunUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"signed"}} {
		if err := run(args, &bytes.Buffer{}); !errors.Is(err, errUsage) {
			t.Errorf("run(%q) = %v, want the usage error", args, err)
		}
	}
}

func TestRunUnreadable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	err := run([]string{missing, missing}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot read "+missing) {
		t.Fatalf("run = %v, want a read failure naming %s", err, missing)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
