package approval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/strazahq/straza/internal/store"
)

// flakyStore fails one repo's reads with an injected error: the fixture for
// "the database went away between the bearer check and the service's own
// device read". Embedding keeps every other repo real.
type flakyStore struct {
	store.Store
	devErr  error
	userErr error
}

type flakyApprovers struct {
	store.ApproverRepo
	err error
}

func (f flakyApprovers) GetDevice(ctx context.Context, id string) (store.ApproverDevice, error) {
	if f.err != nil {
		return store.ApproverDevice{}, f.err
	}
	return f.ApproverRepo.GetDevice(ctx, id)
}

type flakyUsers struct {
	store.UserRepo
	err error
}

func (f flakyUsers) GetByID(ctx context.Context, id string) (store.User, error) {
	if f.err != nil {
		return store.User{}, f.err
	}
	return f.UserRepo.GetByID(ctx, id)
}

func (f *flakyStore) Approvers() store.ApproverRepo {
	return flakyApprovers{f.Store.Approvers(), f.devErr}
}
func (f *flakyStore) Users() store.UserRepo { return flakyUsers{f.Store.Users(), f.userErr} }

// pgDown is the shape of a Postgres outage: Postgres answering SQLSTATE 57P03.
var pgDown = &pgconn.PgError{Severity: "FATAL", Code: "57P03", Message: "the database system is starting up"}

// TestStoreOutageIsNotARevocation pins the service half of outage honesty:
// a store fault during RefreshChallenge, RefreshSigned or DecideSigned
// is ErrStoreUnavailable, never ErrDeviceRevoked or ErrUserInactive (the two
// verdicts that make the approver app destroy its key). A genuinely unknown
// device still reads revoked (control).
func TestStoreOutageIsNotARevocation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Now().Unix()

	// Swap the service's store in place (Service carries a mutex, so no
	// copies) and restore it for the healthy controls at the end.
	withFault := func(dev, user error) *Service {
		h.svc.st = &flakyStore{Store: h.st, devErr: dev, userErr: user}
		return h.svc
	}
	restore := func() { h.svc.st = h.st }

	// Device read down: every lane says unavailable.
	down := withFault(pgDown, nil)
	if _, err := down.RefreshChallenge(ctx, "dev-1"); !errors.Is(err, ErrStoreUnavailable) || errors.Is(err, ErrDeviceRevoked) {
		t.Errorf("RefreshChallenge with the device table down = %v, want ErrStoreUnavailable", err)
	}
	if _, _, err := down.RefreshSigned(ctx, RefreshInput{DeviceID: "dev-1", Challenge: "c", SignatureB64: "x"}); !errors.Is(err, ErrStoreUnavailable) || errors.Is(err, ErrDeviceRevoked) {
		t.Errorf("RefreshSigned with the device table down = %v, want ErrStoreUnavailable", err)
	}
	if _, err := down.DecideSigned(ctx, SignedDecision{RequestID: "r", Verdict: "approve", Challenge: "c", SignatureB64: "x", TS: now, DeviceID: "dev-1", DeciderUserID: "u"}); !errors.Is(err, ErrStoreUnavailable) || errors.Is(err, ErrDeviceRevoked) {
		t.Errorf("DecideSigned with the device table down = %v, want ErrStoreUnavailable", err)
	}

	// User read down on refresh: unavailable, not user_inactive. Needs a real
	// device row so the first read succeeds.
	u, err := h.st.Users().Create(ctx, store.User{Username: "kim", Status: store.UserActive})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := h.st.Approvers().InsertDevice(ctx, store.ApproverDevice{UserID: u.ID, Name: "phone", Platform: "android", KeyAlg: "ES256", PublicKey: "bm90LWEta2V5", KeySecurityLevel: "software", AttestationKind: "none"})
	if err != nil {
		t.Fatal(err)
	}
	userDown := withFault(nil, pgDown)
	if _, _, err := userDown.RefreshSigned(ctx, RefreshInput{DeviceID: dev.ID, Challenge: "c", SignatureB64: "x"}); !errors.Is(err, ErrStoreUnavailable) || errors.Is(err, ErrUserInactive) {
		t.Errorf("RefreshSigned with the user table down = %v, want ErrStoreUnavailable", err)
	}

	// Controls: an unknown device is still a revocation, a healthy store
	// still reaches the key parse (ErrBadKey on the junk key above).
	restore()
	if _, err := h.svc.RefreshChallenge(ctx, "no-such-device"); !errors.Is(err, ErrDeviceRevoked) {
		t.Errorf("unknown device = %v, want ErrDeviceRevoked", err)
	}
	if _, _, err := h.svc.RefreshSigned(ctx, RefreshInput{DeviceID: dev.ID, Challenge: "c", SignatureB64: "x"}); !errors.Is(err, ErrBadKey) {
		t.Errorf("healthy refresh reached = %v, want ErrBadKey (past both reads)", err)
	}
}
