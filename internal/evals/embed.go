package evals

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
)

// builtinFixtures are the committed scenarios under fixtures/, compiled
// into every binary that links this package so `itervox evals recorded`
// works without a repository checkout.
//
//go:embed fixtures
var builtinFixtures embed.FS

// BuiltinFixtures returns the built-in scenarios as an fs.FS rooted at the
// fixture root (<profile>/<scenario>/...). It is read-only.
func BuiltinFixtures() fs.FS {
	sub, err := fs.Sub(builtinFixtures, "fixtures")
	if err != nil { // unreachable: "fixtures" is a valid, embedded path
		panic(err)
	}
	return sub
}

// MaterializeBuiltinFixtures copies the built-in scenarios into a new
// temporary directory, because scenarios are loaded and judged from disk.
// The caller removes the returned directory (cleanup does it). Nothing is
// ever written back into the built-in set: record and promote need a real
// fixture directory.
func MaterializeBuiltinFixtures() (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "itervox-evals-fixtures-*")
	if err != nil {
		return "", nil, fmt.Errorf("evals: built-in fixtures: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	if err := os.CopyFS(dir, BuiltinFixtures()); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("evals: built-in fixtures: %w", err)
	}
	return dir, cleanup, nil
}
