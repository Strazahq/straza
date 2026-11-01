package localapi

// Routes of the git door. Only straza's own git helper calls them. Each
// request names the repo in HeaderGitURL and one helper run in HeaderGitOp,
// beside the harness window headers.
const (
	RouteGit        = Prefix + "/git/"
	RouteGitResolve = RouteGit + "resolve"
	RouteGitGrant   = RouteGit + "grant"
)

// Header names of the git door. HeaderGitURL carries the URL git gave the
// helper, without the straza:: prefix. HeaderGitOp carries 32 random hex
// characters that the helper picks once per run, so the service can find
// the grants that run took.
const (
	HeaderGitURL = "Straza-Git-Url"
	HeaderGitOp  = "Straza-Git-Op"
)

// GitResolveAnswer answers RouteGitResolve. Listed reports whether a Straza
// server lists the repo for git, and when it is false the helper hands the
// command to git's own https transport.
type GitResolveAnswer struct {
	Listed bool `json:"listed"`
}

// GitRef is one ref update a push names: the full ref name and the old and
// new object ids in lower-case hex, with zeros for a new branch.
type GitRef struct {
	Ref string `json:"ref"`
	Old string `json:"old"`
	New string `json:"new"`
}

// GitGrantRequest asks the service for a git grant. Op is "fetch" or
// "push". Refs is set only on a push that names its updates.
type GitGrantRequest struct {
	Op   string   `json:"op"`
	Refs []GitRef `json:"refs,omitempty"`
}

// GitGrantLine is one NDJSON line of the RouteGitGrant answer. Wait lines
// come every few seconds while an approval is open, and the last line holds
// OK or Error.
type GitGrantLine struct {
	Wait  string `json:"wait,omitempty"`
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
}
