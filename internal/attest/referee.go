package attest

// Referee is what a referee record (KindReferee) says about one run of the
// referee itself (`referee lanes`, `audit`, `run`, ...): which build ran,
// with which rules and which policy, on which commits, and what it decided.
// The record's ExitCode is the exit code the referee returned, StartedAt
// and DurationMs when and how long it ran. It never holds file contents,
// environment values or command output: only hashes, commit ids, a path,
// a lane, a count and a check name.
type Referee struct {
	// Version and Commit are the referee build's version and the commit it
	// was built from ("unknown" when the build does not say; a "-dirty"
	// suffix when it was built from a modified tree).
	Version string `json:"version"`
	Commit  string `json:"commit"`
	// Ruleset names the rule tables the build judges with (the default
	// human lane and the audit rules); it changes whenever a rule does.
	Ruleset string `json:"ruleset"`
	// Command is the subcommand that ran (lanes, audit, run).
	Command string `json:"command"`
	// Base is the base ref as given; BaseCommit and HeadCommit the commits
	// it and the head resolved to (empty when the command has none, or
	// failed before resolving it).
	Base       string `json:"base,omitempty"`
	BaseCommit string `json:"base_commit,omitempty"`
	HeadCommit string `json:"head_commit,omitempty"`
	// Policy is the policy file as read from the base commit.
	Policy RefereePolicy `json:"policy"`
	// Lane is the lanes verdict and Findings the audit's finding count.
	// Check and CheckSig name the check record a `run` produced: its Sig
	// binds the two records together.
	Lane     string `json:"lane,omitempty"`
	Findings int    `json:"findings,omitempty"`
	Check    string `json:"check,omitempty"`
	CheckSig string `json:"check_sig,omitempty"`
}

// Policy file states in a referee record.
const (
	// PolicyPresent: the file is on the base commit; SHA256 is its hash.
	PolicyPresent = "present"
	// PolicyAbsent: the base commit has no such file (the defaults apply).
	PolicyAbsent = "absent"
	// PolicyNotRead: there was no base commit to read it from.
	PolicyNotRead = "not_read"
)

// RefereePolicy is the policy file a referee record was judged by.
type RefereePolicy struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	SHA256 string `json:"sha256,omitempty"`
}
