package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteServerPort_ChangesExistingPort(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	content := `---
itervox_schema_version: 2
agent:
  command: claude
server:
  port: 8090
---

# prompt body
`
	if err := os.WriteFile(wf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteServerPort(wf, 0); err != nil {
		t.Fatalf("rewriteServerPort: %v", err)
	}
	data, err := os.ReadFile(wf)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "port: 0") {
		t.Errorf("expected `port: 0` in output; got:\n%s", got)
	}
	if strings.Contains(got, "port: 8090") {
		t.Errorf("old port: 8090 must be replaced; got:\n%s", got)
	}
	if !strings.Contains(got, "# prompt body") {
		t.Errorf("body must be preserved; got:\n%s", got)
	}
}

func TestRewriteServerPort_AppendsWhenServerBlockMissing(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	content := `---
itervox_schema_version: 2
agent:
  command: claude
---

body
`
	if err := os.WriteFile(wf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteServerPort(wf, 8095); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(wf)
	if !strings.Contains(string(data), "port: 8095") {
		t.Errorf("expected `port: 8095` added; got:\n%s", string(data))
	}
}

func TestRewriteServerPort_PortZero(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	content := `---
itervox_schema_version: 2
server:
  port: 8090
---

body
`
	if err := os.WriteFile(wf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteServerPort(wf, 0); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(wf)
	if !strings.Contains(string(data), "port: 0") {
		t.Errorf("expected `port: 0`; got:\n%s", string(data))
	}
}
