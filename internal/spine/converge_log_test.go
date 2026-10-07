package spine

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// TestConvergeApplyLogsReloadAtInfo pins the reload line on the
// consumer's Handle step: another pod's change that this pod converges on
// logs exactly one Info "converge: reloaded from another pod" with
// component, subject and source; our own event, a subject we do not
// converge on, and a failing reload (Error only) write no Info.
func TestConvergeApplyLogsReloadAtInfo(t *testing.T) {
	var buf bytes.Buffer
	c := &ConvergeConsumer{
		conv: Convergence{
			SelfSource: "strazad/self-pod",
			OnPolicy:   func(context.Context) error { return nil },
			OnApps:     func(context.Context) error { return errors.New("boom") },
			OnIdentity: func(context.Context) error { return nil },
		},
		log: slog.New(slog.NewTextHandler(&buf, nil)),
	}
	ctx := context.Background()
	cases := []struct {
		name, subject, source string
		wantInfo, wantError   bool
	}{
		{"other pod policy", "straza.policy.updated", "strazad/other-pod", true, false},
		{"other pod identity", "straza.identity.updated", "strazad/other-pod", true, false},
		{"own event", "straza.policy.updated", "strazad/self-pod", false, false},
		{"not ours", "straza.revocation.user", "strazad/other-pod", false, false},
		{"failing reload", "straza.apps.deployed", "strazad/other-pod", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			c.Handle(ctx, tc.subject, []byte(`{"source":"`+tc.source+`","data":{}}`))
			got := buf.String()
			infos := strings.Count(got, `level=INFO msg="converge: reloaded from another pod"`)
			hasErr := strings.Contains(got, "level=ERROR")
			if (infos == 1) != tc.wantInfo || infos > 1 || hasErr != tc.wantError {
				t.Fatalf("info=%d error=%v, want info=%v error=%v:\n%s", infos, hasErr, tc.wantInfo, tc.wantError, got)
			}
			if tc.wantInfo {
				for _, want := range []string{"component=converge", "subject=" + tc.subject, "source=" + tc.source} {
					if !strings.Contains(got, want) {
						t.Fatalf("record lacks %s:\n%s", want, got)
					}
				}
			}
		})
	}
}
