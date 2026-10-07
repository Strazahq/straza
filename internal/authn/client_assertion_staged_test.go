package authn

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSignAssertionTellsStagedFromNone pins the two refusals a caller must
// tell apart: a deployment with no key, where an administrator has to act,
// and a key that waits for its promotion, where waiting is the whole fix. A
// staged key still answers ErrNoAssertionKey to callers that ask only that.
func TestSignAssertionTellsStagedFromNone(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	keys := testAssertionKeys(t, repo, testSealer(t))
	const client, audience = "sam-sre-agent", "https://idp.example/realms/x"

	check := func(name string, now time.Time, wantStaged, wantNone bool) {
		t.Helper()
		_, err := keys.SignAssertion(now, client, audience)
		if got := errors.Is(err, ErrAssertionKeyStaged); got != wantStaged {
			t.Errorf("%s: errors.Is(err, ErrAssertionKeyStaged) = %v, want %v (err %v)", name, got, wantStaged, err)
		}
		if got := errors.Is(err, ErrNoAssertionKey); got != wantNone {
			t.Errorf("%s: errors.Is(err, ErrNoAssertionKey) = %v, want %v (err %v)", name, got, wantNone, err)
		}
	}

	check("an empty store", time.Now(), false, true)

	first, err := keys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	staged := time.Now()
	check("a staged first key", staged, true, true)

	if _, err := keys.Advance(ctx, staged.Add(2*KeyReloadInterval), caRetireAfter); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	check("a promoted key signs", staged.Add(2*KeyReloadInterval), false, false)

	if _, err := keys.Stage(ctx, staged.Add(2*KeyReloadInterval)); err != nil {
		t.Fatalf("Stage beside the active key: %v", err)
	}
	check("a staged key beside the signing key changes nothing", staged.Add(2*KeyReloadInterval), false, false)

	// Retiring the signing key by hand leaves the staged key of the rotation,
	// so the answer is the staged one until the janitor promotes it.
	if _, _, err := keys.Retire(ctx, time.Now(), first.KID); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	check("the signing key retired by hand beside a staged key", time.Now(), true, true)
}

// TestSignAssertionRetiredByHandIsNotStaged pins the case the key document
// cannot tell: the only key was retired by hand, nothing is staged, and the
// caller must hear that an administrator has to act.
func TestSignAssertionRetiredByHandIsNotStaged(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	keys := testAssertionKeys(t, repo, testSealer(t))
	first, err := keys.Stage(ctx, time.Now())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	now := time.Now().Add(2 * KeyReloadInterval)
	if _, err := keys.Advance(ctx, now, caRetireAfter); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if _, _, err := keys.Retire(ctx, now, first.KID); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	_, err = keys.SignAssertion(now, "sam-sre-agent", "https://idp.example/realms/x")
	if !errors.Is(err, ErrNoAssertionKey) || errors.Is(err, ErrAssertionKeyStaged) {
		t.Errorf("SignAssertion after a retirement by hand = %v, want ErrNoAssertionKey and not ErrAssertionKeyStaged", err)
	}
}

// TestSignAssertionRetiringKeyIsNotStaged pins the third state a caller could
// mistake for a staged key: the only published key is a retiring one, because
// its successor was retired by hand. Nothing will start signing by itself, so
// the answer must send an administrator to rotate.
func TestSignAssertionRetiringKeyIsNotStaged(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	keys := testAssertionKeys(t, repo, testSealer(t))
	if _, err := keys.Stage(ctx, time.Now()); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	now := time.Now().Add(2 * KeyReloadInterval)
	if _, err := keys.Advance(ctx, now, caRetireAfter); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	second, err := keys.Stage(ctx, now)
	if err != nil {
		t.Fatalf("Stage the successor: %v", err)
	}
	now = time.Now().Add(4 * KeyReloadInterval)
	step, err := keys.Advance(ctx, now, time.Hour)
	if err != nil || step.Promoted != second.KID {
		t.Fatalf("Advance = %+v, %v, want the successor %s promoted", step, err, second.KID)
	}
	if _, _, err := keys.Retire(ctx, now, second.KID); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if doc := assertionDoc(t, keys, now); len(doc) != 1 {
		t.Fatalf("the document holds %d keys, want the one retiring key", len(doc))
	}
	_, err = keys.SignAssertion(now, "sam-sre-agent", "https://idp.example/realms/x")
	if !errors.Is(err, ErrNoAssertionKey) || errors.Is(err, ErrAssertionKeyStaged) {
		t.Errorf("SignAssertion beside a retiring key only = %v, want ErrNoAssertionKey and not ErrAssertionKeyStaged", err)
	}
}
