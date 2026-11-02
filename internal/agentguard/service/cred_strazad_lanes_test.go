package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func (f *fakeStrazad) enroll(w http.ResponseWriter, r *http.Request) {
	var body enrollBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.fail(w, http.StatusBadRequest, "malformed JSON body")
		return
	}
	if f.enrollRefuse != "" {
		f.fail(w, http.StatusForbidden, f.enrollRefuse)
		return
	}
	f.mu.Lock()
	nonce, browser := f.idTokens[body.IDToken]
	f.mu.Unlock()
	if (body.IDToken != "idtok-alice" && !browser) || body.Counter != 0 || body.MachineBinding == "" || body.ClientKind != "kit" {
		f.fail(w, http.StatusBadRequest, "bad key enrollment")
		return
	}
	claims, thumb, stale, reason := f.proof(r, "", 0)
	switch {
	case stale:
		f.challenge(w)
		return
	case reason != "" || !f.spend(claims):
		f.fail(w, http.StatusUnauthorized, "the DPoP proof was not accepted: "+reason)
		return
	case browser && !strings.HasPrefix(nonce, thumb+"."):
		f.fail(w, http.StatusForbidden, "Straza refused this sign-in because it was made for a different enrollment.")
		return
	}
	f.mu.Lock()
	f.enrolledNonce = nonce
	for _, d := range f.devices {
		if d.thumb == thumb {
			f.mu.Unlock()
			f.fail(w, http.StatusConflict, "This key is already enrolled for you.")
			return
		}
	}
	d := &fakeDevice{id: "dev-" + randHex(4), thumb: thumb, user: "alice"}
	f.devices[d.id] = d
	f.mu.Unlock()
	f.ok(w, map[string]any{"user_id": "u-alice", "username": "alice", "device_id": d.id, "key_thumbprint": thumb, "sign_in_expires_in": 86400})
}

func (f *fakeStrazad) checkin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDToken      string `json:"id_token"`
		SessionToken string `json:"session_token"`
		DeviceID     string `json:"device_id"`
		Counter      int64  `json:"counter"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.fail(w, http.StatusBadRequest, "malformed JSON body")
		return
	}
	if body.Counter > 0 {
		f.mu.Lock()
		f.received = append(f.received, body.Counter)
		f.mu.Unlock()
	}
	if f.delay != nil {
		if ch := f.delay(body.Counter); ch != nil {
			defer close(f.lateDone)
			<-ch
		}
	}
	switch {
	case body.SessionToken != "":
		f.refresh(w, r, body.SessionToken)
	case body.IDToken != "":
		f.nhi(w, r)
	default:
		f.keyCheckin(w, r, body.DeviceID, body.Counter)
	}
}

func (f *fakeStrazad) keyCheckin(w http.ResponseWriter, r *http.Request, deviceID string, counter int64) {
	if deviceID == "" || counter < 1 {
		f.fail(w, http.StatusBadRequest, "device_id and counter are required")
		return
	}
	claims, thumb, stale, reason := f.proof(r, "", counter)
	switch {
	case stale:
		f.challenge(w)
		return
	case reason != "":
		f.fail(w, http.StatusUnauthorized, "the DPoP proof was not accepted: "+reason)
		return
	}
	f.mu.Lock()
	d := f.devices[deviceID]
	f.mu.Unlock()
	if d == nil || d.thumb != thumb {
		f.fail(w, http.StatusForbidden, "device not enrolled for this key")
		return
	}
	if !f.spend(claims) {
		f.fail(w, http.StatusUnauthorized, "the DPoP proof was not accepted: jti was already used")
		return
	}
	f.mu.Lock()
	refuse := f.checkinRefuse
	f.mu.Unlock()
	if refuse != "" {
		f.fail(w, http.StatusForbidden, refuse)
		return
	}
	f.mu.Lock()
	f.seen = append(f.seen, counter)
	switch {
	case d.revoked:
		f.mu.Unlock()
		f.fail(w, http.StatusForbidden, "device or user revoked")
		return
	case counter <= d.counter:
		d.revoked = true
		f.revoked++
		f.reuse = append(f.reuse, fmt.Sprintf("sent %d, stored %d", counter, d.counter))
		f.mu.Unlock()
		f.fail(w, http.StatusForbidden, "Straza stopped trusting this machine because its key was also used from a copy of it.")
		return
	}
	d.counter = counter
	f.accepted = append(f.accepted, counter)
	f.checkins = append(f.checkins, r.Header.Clone())
	f.mu.Unlock()
	answer := f.mint(d.user, thumb)
	if f.drop != nil && f.drop(counter) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	f.ok(w, answer)
}

func (f *fakeStrazad) refresh(w http.ResponseWriter, r *http.Request, token string) {
	f.mu.Lock()
	ses, ok := f.sessions[token]
	refuse := f.refreshRefuse
	f.refreshes++
	f.mu.Unlock()
	if refuse != "" {
		f.fail(w, http.StatusForbidden, refuse)
		return
	}
	if !ok || !f.clock.Now().Before(ses.exp) {
		f.fail(w, http.StatusUnauthorized, "session token rejected: re-enroll or restart the session")
		return
	}
	claims, thumb, stale, reason := f.proof(r, token, 0)
	switch {
	case stale:
		f.challenge(w)
		return
	case reason != "" || thumb != ses.cnf || !f.spend(claims):
		f.fail(w, http.StatusUnauthorized, "This session token belongs to a key on the machine that checked in.")
		return
	}
	tok := "tok-" + randHex(16)
	ses.exp = f.clock.Now().Add(tokenLife)
	f.mu.Lock()
	f.sessions[tok] = ses
	f.mu.Unlock()
	f.ok(w, f.answer(ses.id, tok, ses.user))
}

func (f *fakeStrazad) nhi(w http.ResponseWriter, r *http.Request) {
	claims, thumb, stale, reason := f.proof(r, "", 0)
	switch {
	case stale:
		f.challenge(w)
		return
	case reason != "" || !f.spend(claims):
		f.fail(w, http.StatusUnauthorized, "the DPoP proof was not accepted: "+reason)
		return
	}
	f.ok(w, f.mint("sam", thumb))
}
