package main

// fatalStartupError marks a run() error that must NOT be retried by the
// outer restart loop. Use cases:
//   - explicit port in WORKFLOW.md is bound by another process — retrying
//     would loop until the operator intervenes;
//   - any future "operator must change config first" startup gate.
//
// Distinct from a transient orchestrator/tracker error that the outer
// loop should patiently re-attempt.
type fatalStartupError struct{ inner error }

func (e fatalStartupError) Error() string { return e.inner.Error() }
func (e fatalStartupError) Unwrap() error { return e.inner }

// isFatalStartupError reports whether err originated from a fatalStartupError
// anywhere in its chain. Used by the restart loop to decide whether to bail
// or keep retrying.
func isFatalStartupError(err error) bool {
	for cur := err; cur != nil; {
		if _, ok := cur.(fatalStartupError); ok {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			return false
		}
		cur = u.Unwrap()
	}
	return false
}
