package main

import (
	"errors"
	"flag"
	"io"
	"strings"
)

// labelFlags collects a repeatable --label flag.
type labelFlags []string

func (l *labelFlags) String() string { return strings.Join(*l, ",") }

func (l *labelFlags) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("a label cannot be empty")
	}
	*l = append(*l, v)
	return nil
}

// createIssueActionBody parses `itervox action create-issue` flags into the
// /agent-actions/{identifier}/create-issue request body. --label (repeatable)
// and --blocked-by-source are optional (#253); the daemon checks each label
// against the profile's create_issue_labels.
func createIssueActionBody(args []string, stderr io.Writer) (map[string]any, error) {
	fs := flag.NewFlagSet("action create-issue", flag.ContinueOnError)
	fs.SetOutput(stderr)
	title := fs.String("title", "", "title for the follow-up issue")
	issueBody := fs.String("body", "", "body/description for the follow-up issue")
	var labels labelFlags
	fs.Var(&labels, "label", "label for the follow-up issue; repeatable; each must be in the profile's create_issue_labels")
	blockedBySource := fs.Bool("blocked-by-source", false, "record the current issue as a blocker of the follow-up issue")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(*title) == "" {
		return nil, errors.New("--title is required")
	}
	body := map[string]any{"title": *title, "body": *issueBody}
	if len(labels) > 0 {
		body["labels"] = []string(labels)
	}
	if *blockedBySource {
		body["blocked_by_source"] = true
	}
	return body, nil
}
