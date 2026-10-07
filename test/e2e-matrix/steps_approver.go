package e2ematrix

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// approverDevice is the signing device a scenario enrolled for one person:
// the P-256 key a browser would keep and the approver credential the server
// minted for it. It lives for one scenario run and never leaves the process.
type approverDevice struct {
	key   *ecdsa.PrivateKey
	token string
}

// approverStep runs the two verbs of the approver action. enroll makes a key
// and enrolls it as a browser with an enroll token the scenario minted, and
// decide signs a verdict on one request the way the self-service page does.
// Both answer through httpStep, so want and save read the server's answer as
// they do on an admin step.
func (s *scenarioRun) approverStep(ctx context.Context, args, want map[string]any) (attempt, error) {
	as, _ := args["as"].(string)
	if action, _ := args["action"].(string); action == "enroll" {
		return s.approverEnroll(ctx, as, args, want)
	}
	return s.approverDecide(ctx, as, args, want)
}

// approverEnroll sends the body the self-service page sends when a person
// enables a browser. The device is kept only when the server minted a
// credential for it, so a scenario may also assert a refused enrollment.
func (s *scenarioRun) approverEnroll(ctx context.Context, as string, args, want map[string]any) (attempt, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return attempt{}, err
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return attempt{}, err
	}
	token, _ := args["token"].(string)
	body := map[string]any{
		"enroll_token": token,
		"device": map[string]any{
			"name": as + "'s browser", "platform": "browser", "key_alg": "ES256",
			"public_key":         base64.StdEncoding.EncodeToString(spki),
			"key_security_level": "software",
			"attestation":        map[string]any{"kind": "none", "blob": ""},
		},
	}
	a, err := s.httpStep(ctx, map[string]any{"method": "POST", "path": "/v1/approver/enroll", "body": body}, want, "")
	if err != nil {
		return a, err
	}
	if minted, ok := lookup(a.body, "device_token"); ok {
		s.approvers[as] = &approverDevice{key: key, token: asString(minted)}
	}
	return a, nil
}

// approverDecide fetches the single-use challenge the server minted for the
// request, signs the decision and posts it. The signed string is the one
// DecideSigned rebuilds: request id, verdict, challenge and timestamp on four
// lines, and the hex SHA-256 of the reason on a fifth when there is one.
func (s *scenarioRun) approverDecide(ctx context.Context, as string, args, want map[string]any) (attempt, error) {
	dev := s.approvers[as]
	if dev == nil {
		return attempt{}, fmt.Errorf("%s has no enrolled device (does an approver enroll step come first?)", as)
	}
	request, _ := args["request"].(string)
	verdict, _ := args["verdict"].(string)
	reason, _ := args["reason"].(string)

	target, err := requestURL(s.stack.server.URL, "/v1/approver/pending")
	if err != nil {
		return attempt{}, err
	}
	code, raw, err := httpCall(ctx, "GET", target, dev.token, "", nil)
	if err != nil {
		return attempt{}, err
	}
	if code != 200 {
		return attempt{detail: fmt.Sprintf("GET /v1/approver/pending: status %d: %s", code, trim(redact(string(raw))))}, nil
	}
	// The decidable queue answers as a bare array of rows.
	var pending []struct {
		ID        string `json:"id"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(raw, &pending); err != nil {
		return attempt{}, fmt.Errorf("GET /v1/approver/pending: %w", err)
	}
	challenge := ""
	for _, row := range pending {
		if row.ID == request {
			challenge = row.Challenge
		}
	}
	if challenge == "" {
		return attempt{detail: fmt.Sprintf("request %s is not among the %d requests %s may decide on the signed lane", request, len(pending), as)}, nil
	}

	ts := time.Now().Unix()
	msg := request + "\n" + verdict + "\n" + challenge + "\n" + strconv.FormatInt(ts, 10)
	if reason != "" {
		sum := sha256.Sum256([]byte(reason))
		msg += "\n" + hex.EncodeToString(sum[:])
	}
	digest := sha256.Sum256([]byte(msg))
	sig, err := ecdsa.SignASN1(rand.Reader, dev.key, digest[:])
	if err != nil {
		return attempt{}, err
	}
	body := map[string]any{"request_id": request, "verdict": verdict, "challenge": challenge, "signature": base64.StdEncoding.EncodeToString(sig), "ts": ts}
	if reason != "" {
		body["reason"] = reason
	}
	return s.httpStep(ctx, map[string]any{"method": "POST", "path": "/v1/approver/decide", "body": body}, want, dev.token)
}
