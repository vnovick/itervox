package referee

import "github.com/vnovick/itervox/internal/referee/adapters"

// DetectTools returns the built-in adapters (sorted by name) whose tool the
// repository at dir looks like it uses: a package.json dependency or the
// tool's config file. It only reads files, and it is how callers outside
// internal/referee (`itervox quickstart`) learn which tools
// `referee envcheck` could check, without importing the adapters package.
func DetectTools(dir string) []string {
	var names []string
	for _, a := range adapters.Builtins() {
		if a.Detect(dir) {
			names = append(names, a.Name())
		}
	}
	return names
}
