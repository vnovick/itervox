package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/vnovick/itervox/internal/atomicfs"
	"gopkg.in/yaml.v3"
)

// rewriteServerPort sets server.port in the WORKFLOW.md front matter to the
// given value (commonly 0 = OS picks a free port, or a distinct explicit
// port per repo) and writes the file atomically. Behaviour:
//
//   - When the `server:` block is absent, it is appended with `port: <n>`.
//   - When `server:` exists but has no `port:` field, the field is added.
//   - When `server:` already has `port:`, the value is replaced.
//
// Used by `itervox init --update --server-port <n>` to migrate stale
// WORKFLOW.md files that pin `server.port: 8090` into the "two daemons in
// parallel" friendly setting without an operator hand-edit.
func rewriteServerPort(workflowPath string, port int) error {
	raw, err := os.ReadFile(workflowPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", workflowPath, err)
	}
	front, after, ok := splitWorkflowFrontMatter(string(raw))
	if !ok {
		return fmt.Errorf("workflow %s: front matter not found", workflowPath)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		return fmt.Errorf("parse front matter: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	srv, _ := doc["server"].(map[string]any)
	if srv == nil {
		srv = map[string]any{}
	}
	srv["port"] = port
	doc["server"] = srv

	encoded, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode front matter: %w", err)
	}
	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(encoded)
	out.WriteString("---\n")
	out.WriteString(after)
	return atomicfs.WriteFile(workflowPath, out.Bytes(), 0o644)
}
