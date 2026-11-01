package objects

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// gateList is a list of ghapp's repos whose ids are "id-" plus the value.
func gateList(values ...string) *List {
	objs := make([]Object, len(values))
	for i, v := range values {
		objs[i] = Object{Value: v, ID: "id-" + v}
	}
	return newList(store.ObjectList{AppID: "app-ghapp", Type: "repo"}, objs)
}

// every, some and none are the three covers lines a holding can carry.
func every(role string) Holding {
	return Holding{Role: role, BindingID: "b-" + role, Scope: CompileScope(&store.ObjectScope{Every: true})}
}

func some(role string, values ...string) Holding {
	s := &store.ObjectScope{}
	for _, v := range values {
		s.Objects = append(s.Objects, store.CoveredObject{ID: "id-" + v, Value: v})
	}
	return Holding{Role: role, BindingID: "b-" + role, Scope: CompileScope(s)}
}

func bare(role string) Holding { return Holding{Role: role, BindingID: "b-" + role} }

const (
	g6RepoCreate = "repo_create on ghapp can reach every repo, so only a role on every repo may call it, and none of your roles on ghapp is one. Ask for a role on every repo, or use a tool that names the repo."
	g7PRCreate   = "pr_create on ghapp changed how it names its repo since an admin pinned it, so roles on some repos cannot use it until an admin publishes the change. Ask an admin to review ghapp's Repos tab."
	g8PRCreate   = "Straza's repo checks are off on this deployment, so only a role on every repo may call pr_create on ghapp. Ask an admin to turn apps.objects.enabled back on, or for a role on every repo."
	g1Tail       = " Check the repo's name, or ask an admin for a role that covers it."
)

// g1 is the uncovered sentence for value, with the covered clause when
// covered is not empty.
func g1(value, covered string) string {
	s := "pr_create on " + value + " was refused: none of your roles on ghapp covers that repo."
	if covered != "" {
		s += " Your roles cover pr_create on " + covered + "."
	}
	return s + g1Tail
}

// TestDecide pins D4 to D9: who grants each reach, which sentence each
// refusal reads, the object the record names and when the list is asked
// to refresh.
func TestDecide(t *testing.T) {
	list := gateList("Strazahq/pay-api", "Strazahq/pay-web", "Strazahq/site")
	argument := manager.ToolReach{Reach: manager.ReachArgument, Argument: "repo"}
	site := json.RawMessage(`{"repo":"Strazahq/site"}`)
	newRepo := json.RawMessage(`{"repo":"Strazahq/new"}`)
	hitSite := &Hit{Type: "repo", Reach: "argument", Value: "Strazahq/site", ID: "id-Strazahq/site"}
	hitNew := &Hit{Type: "repo", Reach: "argument", Value: "Strazahq/new"}
	cases := []struct {
		name string
		call func(c *Call)
		want Verdict
	}{
		{"every grants a listed object", func(c *Call) { c.Held = []Holding{every("dev")} },
			Verdict{Object: hitSite, Grant: ptr(every("dev"))}},
		{"every grants an unlisted object", func(c *Call) { c.Held, c.Args = []Holding{every("dev")}, newRepo },
			Verdict{Object: hitNew, Grant: ptr(every("dev"))}},
		{"every grants before the first listing", func(c *Call) { c.Held, c.List = []Holding{every("dev")}, nil },
			Verdict{Object: &Hit{Type: "repo", Reach: "argument", Value: "Strazahq/site"}, Grant: ptr(every("dev"))}},
		{"a partial role grants its object and sends the id", func(c *Call) { c.Held = []Holding{some("site", "Strazahq/site")} },
			Verdict{Object: hitSite, Grant: ptr(some("site", "Strazahq/site")), SendID: true}},
		{"the first role by name grants", func(c *Call) {
			c.Held = []Holding{some("a-site", "Strazahq/site"), every("b-all")}
		}, Verdict{Object: hitSite, Grant: ptr(some("a-site", "Strazahq/site")), SendID: true}},
		{"an every role first by name grants without the id", func(c *Call) {
			c.Held = []Holding{every("a-all"), some("b-site", "Strazahq/site")}
		}, Verdict{Object: hitSite, Grant: ptr(every("a-all"))}},
		{"a later role grants when the first does not cover", func(c *Call) {
			c.Held = []Holding{some("a-pay", "Strazahq/pay-api"), some("b-site", "Strazahq/site")}
		}, Verdict{Object: hitSite, Grant: ptr(some("b-site", "Strazahq/site")), SendID: true}},
		{"an uncovered object names what the roles cover", func(c *Call) {
			c.Held = []Holding{some("pay", "Strazahq/pay-web", "Strazahq/pay-api")}
		}, Verdict{Object: hitSite, Refusal: g1("Strazahq/site", "Strazahq/pay-api and Strazahq/pay-web"), Checked: []string{"pay"}}},
		{"an unlisted object asks for a listing", func(c *Call) {
			c.Held, c.Args = []Holding{some("pay", "Strazahq/pay-api")}, newRepo
		}, Verdict{Object: hitNew, Refusal: g1("Strazahq/new", "Strazahq/pay-api"), Missed: true, Checked: []string{"pay"}}},
		{"no list yet asks for a listing", func(c *Call) {
			c.Held, c.List = []Holding{some("site", "Strazahq/site")}, nil
		}, Verdict{Object: &Hit{Type: "repo", Reach: "argument", Value: "Strazahq/site"}, Refusal: g1("Strazahq/site", ""), Missed: true, Checked: []string{"site"}}},
		{"a binding with no covers line covers nothing", func(c *Call) { c.Held = []Holding{bare("old")} },
			Verdict{Object: hitSite, Refusal: g1("Strazahq/site", ""), Checked: []string{"old"}}},
		{"no holding refuses", func(c *Call) { c.Held = nil },
			Verdict{Object: hitSite, Refusal: g1("Strazahq/site", "")}},
		{"checked roles are sorted once each", func(c *Call) {
			c.Held = []Holding{some("a", "Strazahq/pay-api"), some("a", "Strazahq/pay-web"), bare("b")}
		}, Verdict{Object: hitSite, Refusal: g1("Strazahq/site", "Strazahq/pay-api and Strazahq/pay-web"), Checked: []string{"a", "b"}}},
		{"checks off refuse a partial role", func(c *Call) {
			c.Held, c.Enabled = []Holding{some("site", "Strazahq/site")}, false
		}, Verdict{Object: hitSite, Refusal: g8PRCreate, Checked: []string{"site"}}},
		{"checks off still let every grant", func(c *Call) {
			c.Held, c.Enabled = []Holding{some("a-site", "Strazahq/site"), every("b-all")}, false
		}, Verdict{Object: hitSite, Grant: ptr(every("b-all"))}},
		{"checks off never ask for a listing", func(c *Call) {
			c.Held, c.Enabled, c.Args = []Holding{some("site", "Strazahq/site")}, false, newRepo
		}, Verdict{Object: hitNew, Refusal: g8PRCreate, Checked: []string{"site"}}},
		{"drift refuses a partial role", func(c *Call) {
			c.Held, c.Drifted = []Holding{some("site", "Strazahq/site")}, true
		}, Verdict{Object: hitSite, Refusal: g7PRCreate, Checked: []string{"site"}}},
		{"drift still lets every grant", func(c *Call) {
			c.Held, c.Drifted = []Holding{some("a-site", "Strazahq/site"), every("b-all")}, true
		}, Verdict{Object: hitSite, Grant: ptr(every("b-all"))}},
		{"unreadable arguments refuse every role too", func(c *Call) {
			c.Held, c.Args = []Holding{every("dev")}, json.RawMessage(`{"repo":"a","repo":"b"}`)
		}, Verdict{Object: &Hit{Type: "repo", Reach: "argument"}, Refusal: g4("a key appears twice"), Checked: []string{"dev"}}},
		{"a missing argument refuses every role too", func(c *Call) {
			c.Held, c.Args = []Holding{every("dev")}, nil
		}, Verdict{Object: &Hit{Type: "repo", Reach: "argument"}, Refusal: g2Repo, Checked: []string{"dev"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Call{Server: "ghapp", Type: "repo", Plural: "repos", Tool: "pr_create", Reach: argument,
				Enabled: true, Args: site, List: list}
			tc.call(&c)
			if got := Decide(c); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Decide\n got %s\nwant %s", show(got), show(tc.want))
			}
		})
	}
}

// TestDecideReach pins D6 for the reaches other than argument.
func TestDecideReach(t *testing.T) {
	cases := []struct {
		name    string
		tool    string
		reach   string
		drifted bool
		held    []Holding
		want    Verdict
	}{
		{"all grants every", "repo_create", manager.ReachAll, false, []Holding{some("a", "Strazahq/site"), every("b")},
			Verdict{Grant: ptr(every("b"))}},
		{"all refuses partial roles", "repo_create", manager.ReachAll, false, []Holding{some("a", "Strazahq/site"), bare("b")},
			Verdict{Object: &Hit{Type: "repo", Reach: "all"}, Refusal: g6RepoCreate, Checked: []string{"a", "b"}}},
		{"credential grants every with the object", "repo_create", manager.ReachCredential, false, []Holding{every("b")},
			Verdict{Object: &Hit{Type: "repo", Reach: "credential"}, Grant: ptr(every("b"))}},
		{"credential refuses partial roles", "repo_create", manager.ReachCredential, false, []Holding{some("a", "Strazahq/site")},
			Verdict{Object: &Hit{Type: "repo", Reach: "credential"}, Refusal: g6RepoCreate, Checked: []string{"a"}}},
		{"none lets the first role grant", "pr_list", manager.ReachNone, false, []Holding{some("a", "Strazahq/site"), every("b")},
			Verdict{Grant: ptr(some("a", "Strazahq/site"))}},
		{"none lets a bare binding grant", "pr_list", manager.ReachNone, false, []Holding{bare("a")},
			Verdict{Grant: ptr(bare("a"))}},
		{"drifted none refuses partial roles", "pr_create", manager.ReachNone, true, []Holding{some("a", "Strazahq/site")},
			Verdict{Object: &Hit{Type: "repo", Reach: "none"}, Refusal: g7PRCreate, Checked: []string{"a"}}},
		{"drifted none lets every grant", "pr_create", manager.ReachNone, true, []Holding{some("a", "Strazahq/site"), every("b")},
			Verdict{Grant: ptr(every("b"))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(Call{Server: "ghapp", Type: "repo", Plural: "repos", Tool: tc.tool,
				Reach: manager.ToolReach{Reach: tc.reach}, Drifted: tc.drifted, Enabled: true,
				Held: tc.held, List: gateList("Strazahq/site")})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Decide\n got %s\nwant %s", show(got), show(tc.want))
			}
		})
	}
}

// TestDecideCoveredClause pins the second sentence of G1: at most ten
// values the caller's partial roles cover that the list holds, sorted,
// then how many more.
func TestDecideCoveredClause(t *testing.T) {
	var values []string
	for i := 0; i < 14; i++ {
		values = append(values, fmt.Sprintf("Strazahq/r%02d", i))
	}
	list := gateList(append([]string{"Strazahq/site"}, values...)...)
	held := []Holding{some("a", values[7:]...), some("b", append(slices.Clone(values[:8]), "Strazahq/gone")...)}
	got := Decide(Call{Server: "ghapp", Type: "repo", Plural: "repos", Tool: "pr_create", Enabled: true,
		Reach: manager.ToolReach{Reach: manager.ReachArgument, Argument: "repo"},
		Args:  json.RawMessage(`{"repo":"Strazahq/site"}`), Held: held, List: list})
	want := g1("Strazahq/site", strings.Join(values[:10], ", ")+" and 4 more")
	if got.Refusal != want {
		t.Errorf("refusal\n got %q\nwant %q", got.Refusal, want)
	}
}

// TestDecideEchoCut pins that the refusal echoes at most 200 bytes of the
// value, cut on a rune boundary.
func TestDecideEchoCut(t *testing.T) {
	long := strings.Repeat("a", 199) + "é" + strings.Repeat("b", 50)
	raw, _ := json.Marshal(map[string]string{"repo": long})
	got := Decide(Call{Server: "ghapp", Type: "repo", Plural: "repos", Tool: "pr_create", Enabled: true,
		Reach: manager.ToolReach{Reach: manager.ReachArgument, Argument: "repo"},
		Args:  raw, Held: []Holding{bare("a")}, List: gateList("Strazahq/site")})
	want := g1(strings.Repeat("a", 199)+"...", "")
	if got.Refusal != want {
		t.Errorf("refusal\n got %q\nwant %q", got.Refusal, want)
	}
	if got.Object == nil || got.Object.Value != long {
		t.Errorf("the record's object must keep the whole value, got %+v", got.Object)
	}
}

// TestCut pins the rune-safe cut both the refusals and the record use.
func TestCut(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
		cut  bool
	}{
		{"abc", 3, "abc", false},
		{"abcd", 3, "abc", true},
		{"abé", 3, "ab", true},
		{"ééé", 4, "éé", true},
	}
	for _, tc := range cases {
		if got, cut := Cut(tc.in, tc.max); got != tc.want || cut != tc.cut {
			t.Errorf("Cut(%q, %d) = (%q, %v), want (%q, %v)", tc.in, tc.max, got, cut, tc.want, tc.cut)
		}
	}
}

func ptr(h Holding) *Holding { return &h }

// show prints a verdict with the holding and hit it points at.
func show(v Verdict) string {
	s := fmt.Sprintf("{Refusal:%q SendID:%v Missed:%v Checked:%v", v.Refusal, v.SendID, v.Missed, v.Checked)
	if v.Grant != nil {
		s += fmt.Sprintf(" Grant:%s/%s", v.Grant.Role, v.Grant.BindingID)
	}
	if v.Object != nil {
		s += fmt.Sprintf(" Object:%+v", *v.Object)
	}
	return s + "}"
}

// BenchmarkDecide decides a covered call against a list of 100,000 objects
// and 200 partial roles, the last of which covers the object.
func BenchmarkDecide(b *testing.B) {
	values := make([]string, 100_000)
	for i := range values {
		values[i] = fmt.Sprintf("Strazahq/repo-%06d", i)
	}
	list := gateList(values...)
	held := make([]Holding, 200)
	for r := range held {
		held[r] = some(fmt.Sprintf("role-%03d", r), values[r*500:r*500+500]...)
	}
	c := Call{Server: "ghapp", Type: "repo", Plural: "repos", Tool: "pr_create", Enabled: true,
		Reach: manager.ToolReach{Reach: manager.ReachArgument, Argument: "repo"},
		Args:  json.RawMessage(`{"repo":"Strazahq/repo-099999","title":"a change"}`), Held: held, List: list}
	b.Run("covered", func(b *testing.B) {
		for b.Loop() {
			if v := Decide(c); v.Refusal != "" {
				b.Fatal(v.Refusal)
			}
		}
	})
	c.Args = json.RawMessage(`{"repo":"Strazahq/unknown","title":"a change"}`)
	b.Run("refused", func(b *testing.B) {
		for b.Loop() {
			if v := Decide(c); v.Refusal == "" {
				b.Fatal("allowed an unknown repo")
			}
		}
	})
}
