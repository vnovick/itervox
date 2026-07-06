package main

// decideInitDepsAnalysis encodes the init-time decision tree for whether to
// run the synchronous one-shot dependency-analysis pass. Extracted so the
// `--analyze auto|always|never` flag behaviour is pure / table-testable.
//
// Rules:
//
//	mode == "always" → run unconditionally (operator override; pass fails
//	                   loudly if credentials are missing).
//	mode == "never"  → skip unconditionally.
//	mode == "auto"   → run only when the .env file looks populated (the
//	                   existing placeholder-heuristic check). This preserves
//	                   the v0.2.0 default that init doesn't fail on a
//	                   fresh project where the operator hasn't filled in
//	                   API keys yet.
func decideInitDepsAnalysis(mode, envPath string) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	default: // "auto" and any unexpected value (validated upstream)
		return !initEnvLooksLikePlaceholder(envPath)
	}
}
