package hostclass

import (
	"net/netip"
	"testing"
)

// TestClasses pins the class of each address family the sentences name,
// and that Refused passes a private or public address and reads a NAT64 or
// 6to4 address as the IPv4 address it carries.
func TestClasses(t *testing.T) {
	t.Parallel()
	cases := []struct{ addr, class, refused string }{
		{"127.0.0.1", Loopback, Loopback},
		{"::1", Loopback, Loopback},
		{"::ffff:127.0.0.1", Loopback, Loopback},
		{"64:ff9b::7f00:1", "", Loopback},
		{"2002:7f00:1::1", "", Loopback},
		{"0.0.0.0", Unspecified, Unspecified},
		{"::", Unspecified, Unspecified},
		{"169.254.1.1", LinkLocal, LinkLocal},
		{"fe80::1%eth0", LinkLocal, LinkLocal},
		{"169.254.169.254", Metadata, Metadata},
		{"::ffff:169.254.169.254", Metadata, Metadata},
		{"fd00:ec2::254", Metadata, Metadata},
		{"100.100.100.200", Metadata, Metadata},
		{"64:ff9b::a9fe:a9fe", "", Metadata},
		{"2002:a9fe:a9fe::1", "", Metadata},
		{"10.1.2.3", Private, ""},
		{"172.16.0.1", Private, ""},
		{"192.168.1.10", Private, ""},
		{"100.64.0.1", Private, ""},
		{"fd12::1", Private, ""},
		{"::ffff:10.0.0.1", Private, ""},
		{"93.184.216.34", "", ""},
		{"2001:db8::1", "", ""},
		{"64:ff9b::5db8:d822", "", ""},
		{"2002:5db8:d822::1", "", ""},
	}
	for _, tc := range cases {
		addr := netip.MustParseAddr(tc.addr)
		if got := Addr(addr); got != tc.class {
			t.Errorf("Addr(%s) = %q, want %q", tc.addr, got, tc.class)
		}
		if got := Refused(addr); got != tc.refused {
			t.Errorf("Refused(%s) = %q, want %q", tc.addr, got, tc.refused)
		}
	}
}
