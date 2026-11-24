package entrasync

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/store"
)

func TestDescription(t *testing.T) {
	const tail = " Straza created this group and manages it. Do not rename or delete it."
	cases := []struct {
		name string
		role store.Role
		want string
	}{
		{"a role with a description",
			store.Role{Name: "invoice-agents", Kind: store.RoleKindApplication, Description: "Lets invoice agents call the tools of the objects-demo server."},
			"Members of this group hold the Straza role invoice-agents. Lets invoice agents call the tools of the objects-demo server." + tail},
		{"a role with no description",
			store.Role{Name: "straza-admin", Kind: store.RoleKindApplication},
			"Members of this group hold the Straza role straza-admin." + tail},
		{"a description of blanks counts as none",
			store.Role{Name: "readers", Kind: store.RoleKindBusiness, Description: " \n "},
			"Members of this group hold the Straza role readers." + tail},
		{"a description with no full stop gets one",
			store.Role{Name: "finance-readers", Kind: store.RoleKindBusiness, Description: "reads finance-readers"},
			"Members of this group hold the Straza role finance-readers. reads finance-readers." + tail},
		{"a description that ends in a question mark keeps it",
			store.Role{Name: "triage", Kind: store.RoleKindBusiness, Description: "Who reads the tickets?"},
			"Members of this group hold the Straza role triage. Who reads the tickets?" + tail},
		{"spaces around a description are dropped",
			store.Role{Name: "readers", Kind: store.RoleKindBusiness, Description: "  Reads the reports.\n"},
			"Members of this group hold the Straza role readers. Reads the reports." + tail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := description(c.role); got != c.want {
				t.Errorf("description = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDescriptionCap(t *testing.T) {
	const (
		head = "Members of this group hold the Straza role invoice-agents. "
		tail = " Straza created this group and manages it. Do not rename or delete it."
	)
	fits := strings.Repeat("a", descriptionCap-len(head)-len(tail)-1) + "."
	cases := []struct {
		name, desc string
		cut        bool
	}{
		{"a description that just fits is kept whole", fits, false},
		{"a description one byte over is cut", "a" + fits, true},
		{"a long description is cut", strings.Repeat("a", 2000), true},
		{"a long description of two-byte letters is cut on a letter", strings.Repeat("ž", 1000), true},
		{"a long description with no full stop is cut", strings.Repeat("word ", 400), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := description(store.Role{Name: "invoice-agents", Kind: store.RoleKindApplication, Description: c.desc})
			if len(got) > descriptionCap {
				t.Errorf("the description holds %d bytes, over the cap of %d", len(got), descriptionCap)
			}
			if !utf8.ValidString(got) {
				t.Errorf("the description is not valid UTF-8: %q", got[len(got)-90:])
			}
			if !strings.HasPrefix(got, head) || !strings.HasSuffix(got, tail) {
				t.Errorf("the description lost a fixed sentence: %q", got)
			}
			if c.cut != strings.HasSuffix(got, "..."+tail) {
				t.Errorf("cut = %v, want %v: %q", !c.cut, c.cut, got[len(got)-90:])
			}
			if !c.cut && got != head+c.desc+tail {
				t.Errorf("a description that fits was changed: %q", got)
			}
		})
	}
}
