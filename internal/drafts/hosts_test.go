package drafts

import (
	"net/netip"
	"testing"
)

func TestHostOf(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://api.github.com/mcp":              "api.github.com",
		"https://GitHub.COM:443/x":                "GitHub.COM",
		"https://user:pw@h.example/":              "h.example",
		"http://[::1]:8080/mcp":                   "::1",
		"https://g%D1%96thub.com/x":               "g\u0456thub.com",
		"https://g\u0456thub.com/%zz":             "g\u0456thub.com",
		"https://u@[fd00:ec2::254]:80/%zz":        "fd00:ec2::254",
		"https://h.example:8443?q=%zz":            "h.example",
		"not an address":                          "",
		"":                                        "",
		"https://xn--gthub-n4a.com/mcp?token=a%z": "xn--gthub-n4a.com",
	}
	for raw, want := range cases {
		if got := hostOf(raw); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestASCIIHost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		host, want string
		fails      bool
	}{
		{"GitHub.com.", "github.com", false},
		{"g\u0456thub.com", "xn--gthub-n2e.com", false},
		{"\uff27\uff29\uff34\uff28\uff35\uff22.com", "github.com", false},
		{"FD00:EC2::254", "fd00:ec2::254", false},
		{"a_b.example", "a_b.example", false},
		{"xn--api-.example.com", "xn--api-.example.com", false},
		{"XN--GITHUB-.com", "xn--github-.com", false},
		{"g\u0456thub_x.com", "g\u0456thub_x.com", true},
	}
	for _, tc := range cases {
		got, err := asciiHost(tc.host)
		if got != tc.want || (err != nil) != tc.fails {
			t.Errorf("asciiHost(%q) = %q, %v, want %q with an error %v", tc.host, got, err, tc.want, tc.fails)
		}
	}
}

func TestUnicodeHost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		host, want string
		ok         bool
	}{
		{"xn--gthub-n4a.com", "g\u0131thub.com", true},
		{"api.XN--GTHUB-N4A.com", "api.g\u0131thub.com", true},
		{"github.com", "", false},
		{"xn--zz-.com", "zz.com", true},
		{"xn--a.com", "", false},
	}
	for _, tc := range cases {
		if got, ok := unicodeHost(tc.host); got != tc.want || ok != tc.ok {
			t.Errorf("unicodeHost(%q) = %q, %v, want %q, %v", tc.host, got, ok, tc.want, tc.ok)
		}
	}
}

// TestHostClass pins the classes a request from strazad should not reach,
// the cloud metadata addresses first, and a mapped IPv4 address read as its
// IPv4 address.
func TestHostClass(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"localhost":                "loopback",
		"api.localhost.":           "loopback",
		"127.0.0.1":                "loopback",
		"::1":                      "loopback",
		"::ffff:127.0.0.1":         "loopback",
		"0.0.0.0":                  "unspecified",
		"::":                       "unspecified",
		"10.1.2.3":                 "private",
		"192.168.1.10":             "private",
		"fd12::1":                  "private",
		"100.64.0.1":               "private",
		"169.254.1.1":              "link-local",
		"fe80::1%eth0":             "link-local",
		"169.254.169.254":          "cloud metadata",
		"::ffff:169.254.169.254":   "cloud metadata",
		"fd00:ec2::254":            "cloud metadata",
		"100.100.100.200":          "cloud metadata",
		"metadata.google.internal": "cloud metadata",
		"api.github.com":           "",
		"8.8.8.8":                  "",
		"2001:4860:4860::8888":     "",
	}
	for host, want := range cases {
		if got := hostClass(host); got != want {
			t.Errorf("hostClass(%q) = %q, want %q", host, got, want)
		}
	}
	if got := addrClass(netip.MustParseAddr("::ffff:10.0.0.1")); got != "private" {
		t.Errorf("addrClass of a mapped private address = %q", got)
	}
}

func TestForeignLetter(t *testing.T) {
	t.Parallel()
	if r, ok := foreignLetter("g\u0456thub.com"); !ok || r != '\u0456' {
		t.Errorf("foreignLetter found %q, %v, want the Cyrillic i", r, ok)
	}
	if _, ok := foreignLetter("xn--gthub-n2e.com"); ok {
		t.Error("foreignLetter refused a host in its xn-- form")
	}
}

func TestVisibleSpellsWhatPrintsNothing(t *testing.T) {
	t.Parallel()
	if got := visible("dev\u202eved"); got != "devU+202Eved" {
		t.Errorf("visible = %q", got)
	}
	if got := runeWords('\u0456'); got != "\u0456 (U+0456)" {
		t.Errorf("runeWords of a letter = %q", got)
	}
	if got := runeWords('\u200b'); got != "U+200B" {
		t.Errorf("runeWords of an invisible space = %q", got)
	}
}
