package objects

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/strazahq/straza/internal/manager"
)

// TestCoversAgreesWithDecide pins that Covers tells whoami what Decide
// does: for every reach, drift, checks setting and holding mix, a call
// that names a listed object or an unlisted one passes the gate exactly
// when Covers says the tool covers every object or lists that object.
func TestCoversAgreesWithDecide(t *testing.T) {
	list := gateList("Strazahq/docs", "Strazahq/pay-api", "Strazahq/site")
	holdings := map[string][]Holding{
		"every":          {every("a")},
		"some":           {some("a", "Strazahq/docs", "Strazahq/site")},
		"some and every": {some("a", "Strazahq/docs"), every("b")},
		"bare":           {bare("a")},
		"some unlisted":  {some("a", "Strazahq/gone")},
	}
	reaches := []manager.ToolReach{
		{Reach: manager.ReachArgument, Argument: "repo"},
		{Reach: manager.ReachAll}, {Reach: manager.ReachCredential}, {Reach: manager.ReachNone},
	}
	for name, held := range holdings {
		for _, reach := range reaches {
			for _, drifted := range []bool{false, true} {
				for _, enabled := range []bool{true, false} {
					c := Call{Server: "ghapp", Type: "repo", Plural: "repos", Tool: "pr_create", Reach: reach,
						Drifted: drifted, Enabled: enabled, Held: held, List: list}
					cov := c.Covers()
					for _, value := range []string{"Strazahq/docs", "Strazahq/pay-api", "Strazahq/site", "Strazahq/new"} {
						args, _ := json.Marshal(map[string]string{"repo": value})
						c.Args = args
						passes := Decide(c).Refusal == ""
						want := !cov.Named || cov.Every || slices.Contains(cov.First, value)
						if passes != want {
							t.Errorf("%s, reach %s, drifted %v, checks %v, %s: Decide passes %v, Covers %+v",
								name, reach.Reach, drifted, enabled, value, passes, cov)
						}
					}
					if cov.Count != len(cov.First) {
						t.Errorf("%s, reach %s: count %d for %d values under the cap", name, reach.Reach, cov.Count, len(cov.First))
					}
				}
			}
		}
	}
}

// TestCoversCapsTheNames pins that Covers names at most coveredMax objects
// and counts the rest.
func TestCoversCapsTheNames(t *testing.T) {
	var values []string
	for i := range coveredMax + 3 {
		values = append(values, "Strazahq/r"+string(rune('a'+i)))
	}
	c := Call{Server: "ghapp", Type: "repo", Tool: "pr_create", Enabled: true,
		Reach: manager.ToolReach{Reach: manager.ReachArgument, Argument: "repo"},
		Held:  []Holding{some("a", values...)}, List: gateList(values...)}
	cov := c.Covers()
	if !cov.Named || cov.Every || len(cov.First) != coveredMax || cov.Count != coveredMax+3 {
		t.Errorf("Covers = %+v, want %d names of %d", cov, coveredMax, coveredMax+3)
	}
}
