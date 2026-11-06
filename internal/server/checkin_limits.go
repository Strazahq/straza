package server

import (
	"fmt"
	"strings"
)

// checkinTextMax is the most bytes of a harness name, a harness version or
// a client version that a check-in may carry. A straza client sends a few
// dozen, and each one rides on the session, its token and, for the harness,
// every audit record of the session.
const checkinTextMax = 256

// The refusals of a check-in whose text is over checkinTextMax bytes, each
// with the length sent and the bound.
const (
	harnessNameTooLongMsg = "This check-in names a harness of %d bytes, and strazad accepts at most %d, " +
		"because policies match the harness name and every audit record of the session carries it. " +
		"A straza client sends the name of the harness it runs under, which is never that long, " +
		"so a program other than a straza client likely made this check-in. Find out what sent it, or reinstall straza."
	harnessVersionTooLongMsg = "This check-in names a harness version of %d bytes, and strazad accepts at most %d, " +
		"because every audit record of the session carries it. " +
		"A straza client passes on the version its harness reports, which is never that long, " +
		"so the harness or a program other than a straza client likely sent it. Update the harness, or find out what made this check-in."
	clientVersionTooLongMsg = "This check-in names a straza client version of %d bytes, and strazad accepts at most %d, " +
		"because the session keeps it and the console shows it. " +
		"A straza client sends its own build version, which is never that long, " +
		"so a program other than a straza client likely made this check-in. Find out what sent it, or reinstall straza."
)

// checkinNULMsg is the refusal of a check-in whose text holds a NUL
// character, with the name of the field that holds it and the next step,
// nulStepStraza or nulStepHarness.
const checkinNULMsg = "The %s field of this check-in holds a NUL character (a zero byte), and strazad does not accept one. " +
	"strazad stores this field when a check-in starts a session, and a Postgres store cannot hold that character, " +
	"so strazad refuses it in every check-in. %s"

// The next steps of a NUL refusal. A straza client sets the harness name,
// its own version and commit, and the attestation hashes itself. It copies
// the harness version from the harness.
const (
	nulStepStraza = "A straza client never puts one there, so a program other than a straza client likely made this check-in. " +
		"Find out what made it, or reinstall straza."
	nulStepHarness = "A straza client passes on the version its harness reports, so the harness or a program other than a straza client likely sent it. " +
		"Update the harness, or find out what made this check-in."
)

// checkinTextRefusal answers the refusal of req when its harness name,
// harness version or client version is over checkinTextMax bytes, or when
// one of them, the client commit or a key or value of the attestation hashes
// holds a NUL character, and "" when all fit. These are the fields the
// session keeps. It refuses and never cuts or strips, because policies match
// the harness name as sent, a cut name could match a rule the real one does
// not, and a stripped field would make the session record a value the
// client never sent.
func checkinTextRefusal(req checkinRequest) string {
	switch {
	case len(req.Harness.Name) > checkinTextMax:
		return fmt.Sprintf(harnessNameTooLongMsg, len(req.Harness.Name), checkinTextMax)
	case len(req.Harness.Version) > checkinTextMax:
		return fmt.Sprintf(harnessVersionTooLongMsg, len(req.Harness.Version), checkinTextMax)
	case len(req.Client.Version) > checkinTextMax:
		return fmt.Sprintf(clientVersionTooLongMsg, len(req.Client.Version), checkinTextMax)
	case strings.Contains(req.Harness.Name, "\x00"):
		return fmt.Sprintf(checkinNULMsg, "harness.name", nulStepStraza)
	case strings.Contains(req.Harness.Version, "\x00"):
		return fmt.Sprintf(checkinNULMsg, "harness.version", nulStepHarness)
	case strings.Contains(req.Client.Version, "\x00"):
		return fmt.Sprintf(checkinNULMsg, "client.version", nulStepStraza)
	case strings.Contains(req.Client.Commit, "\x00"):
		return fmt.Sprintf(checkinNULMsg, "client.commit", nulStepStraza)
	}
	for artifact, hash := range req.Attestation.Hashes {
		if strings.Contains(artifact, "\x00") || strings.Contains(hash, "\x00") {
			return fmt.Sprintf(checkinNULMsg, "attestation.hashes", nulStepStraza)
		}
	}
	return ""
}
