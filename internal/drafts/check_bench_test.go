package drafts

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// largeTenant is a World the size of a large tenant with the diversity of
// a real one: 10,000 users, each holding one to three of 50
// business roles picked by a fixed hash, a fifth of them autonomous
// agents; 450 application roles, each with an access row on one of 20
// running servers of 50 tools, 1,000 tools in all; the business roles each
// composing 9 of them; 200 sets that gate an application role each; and an
// approver pool. That is 501 roles.
func largeTenant() World {
	w := World{Apps: map[string]App{}, Roles: map[string]Role{}, Implies: map[string][]string{}, Access: map[string]Access{},
		Policies: map[string]Policy{}, Holders: map[string][]Holder{}, Fingerprints: map[string]Fingerprint{},
		LocalToolDefault: policy.EffectAllow, Push: true, DockerOnPath: true, SnapshotID: "snap-large"}
	tools := make([]string, 50)
	for j := range tools {
		tools[j] = fmt.Sprintf("tool%02d", j)
	}
	for s := range 20 {
		name := fmt.Sprintf("srv%02d", s)
		w.Apps[name] = App{ID: name, Name: name, Status: "running", Runtime: "remote", URL: "https://" + name + ".example.com/mcp",
			Credential: CredentialNone, Offered: tools, Exposure: []string{"*"}, AdminRole: "mcp-admin-" + name, RolePrefix: name + "-"}
	}
	w.Roles["approvers"] = Role{ID: "approvers", Name: "approvers", Kind: RoleKindApprover, Plane: PlaneAccess}
	for i := range 450 {
		name := fmt.Sprintf("app%03d", i)
		w.Roles[name] = Role{ID: name, Name: name, Kind: RoleKindApplication, Plane: PlaneAccess}
		row := []string{"*"}
		if i%4 != 0 {
			row = tools[i%5*10 : i%5*10+10]
		}
		w.Access[name] = Access{ID: "b-" + name, Server: fmt.Sprintf("srv%02d", i%20), Tools: row}
		if i < 200 {
			set := name + "-access"
			w.Policies[set] = Policy{Name: set, Text: gainSet(set, "{ roles: ["+name+"] }",
				"    - id: hold\n      tools: [mcp.call]\n      toolNames: { allow: [tool01, tool02, tool03, tool11, tool12] }\n      mode: approve\n"+
					"      approve: { roles: [approvers], timeoutSeconds: 300 }\n    - id: no-49\n      tools: [mcp.call]\n      toolNames: { deny: [tool49] }\n")}
		}
	}
	for b := range 50 {
		name := fmt.Sprintf("biz%02d", b)
		w.Roles[name] = Role{ID: name, Name: name, Kind: RoleKindBusiness, Plane: PlaneAccess}
		for k := range 9 {
			w.Implies[name] = append(w.Implies[name], fmt.Sprintf("app%03d", b*9+k))
		}
	}
	for u := range 10000 {
		h := Holder{Username: fmt.Sprintf("user%05d", u), UserType: "human", AgencyMode: "interactive", Devices: 1}
		if u%5 == 0 {
			h = Holder{Username: h.Username, Agent: true, UserType: "agent", AgencyMode: "autonomous", Sponsor: fmt.Sprintf("user%05d", u+1)}
		}
		picked := map[int]bool{}
		x := uint32(u)*2654435761 + 12345
		for len(picked) < 1+u%3 {
			x = x*1103515245 + 12345
			if b := int(x>>16) % 50; !picked[b] {
				picked[b] = true
				w.Holders[fmt.Sprintf("biz%02d", b)] = append(w.Holders[fmt.Sprintf("biz%02d", b)], h)
			}
		}
		if u < 20 {
			w.Holders["approvers"] = append(w.Holders["approvers"], h)
		}
	}
	for name := range w.Roles {
		w.Fingerprints["Role/"+name] = Fingerprint("fp-" + name)
	}
	for name := range w.Policies {
		w.Fingerprints["PolicySet/"+name] = Fingerprint("fp-" + name)
	}
	return w
}

// largeDrafts are the drafts the budget reads: a typical one that changes
// one business role and one set, and the widest, a set every session
// matches, which touches every role.
func largeDrafts(w World) map[string][]Item {
	implies := make([]string, 0, 10)
	for k := range 10 {
		implies = append(implies, fmt.Sprintf("app%03d", k))
	}
	return map[string][]Item{
		"typical": {intakeRole("biz00", "    kind: business\n    implies: ["+strings.Join(implies, ", ")+"]\n"),
			{Kind: KindPolicySet, Name: "app000-access", Op: OpPut, Doc: strings.Replace(w.Policies["app000-access"].Text, "timeoutSeconds: 300", "timeoutSeconds: 900", 1)}},
		"widest": {{Kind: KindPolicySet, Name: "everyone", Op: OpPut, Doc: gainSet("everyone", "{}",
			"    - id: hold-20\n      tools: [mcp.call]\n      toolNames: { allow: [tool20] }\n      mode: approve\n      approve: { roles: [approvers] }\n")}},
	}
}

// bulkTenant is a tenant of n application roles, each with a row on one
// server and a set of its own that holds two tools for approvers, the
// shape a sync of many roles writes.
func bulkTenant(n int) World {
	w := World{Apps: map[string]App{}, Roles: map[string]Role{}, Implies: map[string][]string{}, Access: map[string]Access{},
		Policies: map[string]Policy{}, Holders: map[string][]Holder{}, Fingerprints: map[string]Fingerprint{},
		LocalToolDefault: policy.EffectAllow, Push: true, DockerOnPath: true, SnapshotID: "snap-bulk"}
	w.Apps["srv"] = App{ID: "srv", Name: "srv", Status: "running", Runtime: "remote", URL: "https://srv.example.com/mcp", Credential: CredentialNone,
		Offered: []string{"t1", "t2", "t3"}, Exposure: []string{"*"}}
	w.Roles["approvers"] = Role{ID: "approvers", Name: "approvers", Kind: RoleKindApprover, Plane: PlaneAccess}
	for i := range n {
		name := fmt.Sprintf("app%04d", i)
		w.Roles[name] = Role{ID: name, Name: name, Kind: RoleKindApplication, Plane: PlaneAccess}
		w.Access[name] = Access{ID: "b-" + name, Server: "srv", Tools: []string{"*"}}
		w.Holders[name] = []Holder{{Username: "user-" + name, UserType: "human", AgencyMode: "interactive"}}
		set := name + "-access"
		w.Policies[set] = Policy{Name: set, Text: gainSet(set, "{ roles: ["+name+"] }",
			"    - id: hold\n      tools: [mcp.call]\n      toolNames: { allow: [t1, t2] }\n      mode: approve\n      approve: { roles: [approvers], timeoutSeconds: 300 }\n")}
		w.Fingerprints["PolicySet/"+set] = Fingerprint("fp-" + set)
		w.Fingerprints["Role/"+name] = Fingerprint("fp-" + name)
	}
	return w
}

// bulkDraft loosens the hold of the first k sets of bulkTenant.
func bulkDraft(w World, k int) []Item {
	items := make([]Item, 0, k)
	for i := range k {
		set := fmt.Sprintf("app%04d-access", i)
		items = append(items, Item{Kind: KindPolicySet, Name: set, Op: OpPut, Doc: strings.Replace(w.Policies[set].Text, "timeoutSeconds: 300", "timeoutSeconds: 900", 1)})
	}
	return items
}

// TestCheckStaysInItsBudget pins the budget of a check, the time a publish
// holds the config mutex through its verdict: over largeTenant a typical
// draft under 1 second and the widest under 5, and a sync that changes 200
// sets of 2,000 under 5, each with its memory bounded, ten times that
// under the race detector.
func TestCheckStaysInItsBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("reads a tenant of 10,000 users")
	}
	w, bulk := largeTenant(), bulkTenant(2000)
	scale := time.Duration(1)
	if raceOn {
		scale = 10
	}
	drafts := largeDrafts(w)
	for _, tc := range []struct {
		name  string
		w     World
		items []Item
		limit time.Duration
		bytes uint64
	}{
		{"typical", w, drafts["typical"], time.Second, 256 << 20},
		{"widest", w, drafts["widest"], 5 * time.Second, 1 << 30},
		{"200 changed sets of 2,000", bulk, bulkDraft(bulk, 200), 5 * time.Second, 1 << 30},
	} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		v := Check(tc.w, stamped(tc.w, tc.items...), CheckInput{Now: checkNow})
		took := time.Since(start)
		runtime.ReadMemStats(&after)
		if len(v.Refused) > 0 || len(v.Gains) == 0 {
			t.Fatalf("%s: refused %q with %d gains", tc.name, findingLines(v.Refused), len(v.Gains))
		}
		if allocated := after.TotalAlloc - before.TotalAlloc; took > tc.limit*scale || allocated > tc.bytes*uint64(scale) {
			t.Errorf("%s: Check took %v and allocated %d MiB, over %v and %d MiB", tc.name, took, allocated>>20, tc.limit*scale, tc.bytes*uint64(scale)>>20)
		}
	}
}

// BenchmarkCheckLargeTenant times Check for the drafts of the budget, and
// for the typical draft on a direct route while admin.secondPerson is off.
func BenchmarkCheckLargeTenant(b *testing.B) {
	w, bulk := largeTenant(), bulkTenant(2000)
	drafts := largeDrafts(w)
	for _, tc := range []struct {
		name         string
		w            World
		items        []Item
		refusalsOnly bool
	}{
		{"typical", w, drafts["typical"], false},
		{"widest", w, drafts["widest"], false},
		{"typical, refusals only", w, drafts["typical"], true},
		{"200 changed sets of 2,000", bulk, bulkDraft(bulk, 200), false},
	} {
		b.Run(tc.name, func(b *testing.B) {
			d := stamped(tc.w, tc.items...)
			v := Check(tc.w, d, CheckInput{RefusalsOnly: tc.refusalsOnly})
			if len(v.Refused) > 0 {
				b.Fatalf("refused %q", findingLines(v.Refused))
			}
			b.ReportMetric(float64(len(v.Gains)), "gains")
			b.ResetTimer()
			for range b.N {
				Check(tc.w, d, CheckInput{RefusalsOnly: tc.refusalsOnly})
			}
		})
	}
}
