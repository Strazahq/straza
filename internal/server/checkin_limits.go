package server

import "fmt"

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

// checkinTextRefusal answers the refusal of req when its harness name,
// harness version or client version is over checkinTextMax bytes, and ""
// when all three fit. It refuses and never cuts, because policies match the
// harness name as sent, and a cut name could match a rule the real one does
// not.
func checkinTextRefusal(req checkinRequest) string {
	switch {
	case len(req.Harness.Name) > checkinTextMax:
		return fmt.Sprintf(harnessNameTooLongMsg, len(req.Harness.Name), checkinTextMax)
	case len(req.Harness.Version) > checkinTextMax:
		return fmt.Sprintf(harnessVersionTooLongMsg, len(req.Harness.Version), checkinTextMax)
	case len(req.Client.Version) > checkinTextMax:
		return fmt.Sprintf(clientVersionTooLongMsg, len(req.Client.Version), checkinTextMax)
	}
	return ""
}
