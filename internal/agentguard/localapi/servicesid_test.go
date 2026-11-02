//go:build !darwin

package localapi

import (
	"crypto/sha1"
	"encoding/binary"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

// serviceSID computes the SID Windows gives NT SERVICE\<name>: S-1-5-80
// and five little-endian 32-bit words of the SHA-1 of the upper-case name
// in UTF-16LE.
func serviceSID(name string) string {
	units := utf16.Encode([]rune(strings.ToUpper(name)))
	raw := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(raw[2*i:], u)
	}
	sum := sha1.Sum(raw)
	sid := "S-1-5-80"
	for i := range 5 {
		sid += "-" + strconv.FormatUint(uint64(binary.LittleEndian.Uint32(sum[4*i:])), 10)
	}
	return sid
}

// TestServiceSIDIsTheServiceAccounts pins ServiceSID to the account of the
// service named ServiceAccount, with TrustedInstaller's well-known SID as
// the positive control that the computation is Windows' own.
func TestServiceSIDIsTheServiceAccounts(t *testing.T) {
	for _, row := range []struct{ name, want string }{
		{"TrustedInstaller", "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"},
		{ServiceAccount, ServiceSID},
	} {
		if got := serviceSID(row.name); got != row.want {
			t.Errorf("SID of NT SERVICE\\%s = %s, want %s", row.name, got, row.want)
		}
	}
}
