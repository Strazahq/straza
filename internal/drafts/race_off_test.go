//go:build !race

package drafts

// raceOn is true in a build with the race detector, which slows the YAML
// decoder about tenfold.
const raceOn = false
