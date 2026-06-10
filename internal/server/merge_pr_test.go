package server

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeGHResponse struct {
	out []byte
	err error
}

func fakeGH(responses map[string]fakeGHResponse) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		r, ok := responses[key]
		if !ok {
			return nil, errors.New("fake gh: no canned response for: " + key)
		}
		return r.out, r.err
	}
}

func TestMergePRGate_InvalidStrategy(t *testing.T) {
	gate := MergePRGate{Strategy: "yolo", GH: fakeGH(nil)}
	_, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(reason, MergePRReasonInvalidStrategy) {
		t.Errorf("expected invalid_strategy reason; got %q", reason)
	}
}

func TestMergePRGate_BlockedLabelRefusesMerge(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[{"name":"migration"}],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
	})
	gate := MergePRGate{
		Strategy:    "squash",
		BlockLabels: []string{"migration"},
		GH:          gh,
	}
	_, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(reason, MergePRReasonBlockedLabel) {
		t.Errorf("expected blocked_label reason; got %q", reason)
	}
}

func TestMergePRGate_NotMergeableRefusesMerge(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[],"mergeable":"CONFLICTING","mergeStateStatus":"DIRTY","state":"OPEN"}`),
		},
	})
	gate := MergePRGate{Strategy: "squash", GH: gh}
	_, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(reason, MergePRReasonNotMergeable) {
		t.Errorf("expected not_mergeable reason; got %q", reason)
	}
}

func TestMergePRGate_RequiredChecksFailingRefusesMerge(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
		"pr checks 7 --required": {
			out: []byte("ci.lint failing\n"),
			err: errors.New("exit status 1"),
		},
	})
	gate := MergePRGate{Strategy: "squash", GH: gh}
	_, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(reason, MergePRReasonChecksFailed) {
		t.Errorf("expected checks_failed reason; got %q", reason)
	}
}

func TestMergePRGate_HappyPathMergesAndReturnsCommit(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"OPEN"}`),
		},
		"pr checks 7 --required": {
			out: []byte("all passing\n"),
		},
		"pr merge 7 --squash --delete-branch": {
			out: []byte("Merged pull request #7\n"),
		},
		"pr view 7 --json mergeCommit": {
			out: []byte(`{"mergeCommit":{"oid":"abc1234"}}`),
		},
	})
	gate := MergePRGate{Strategy: "squash", GH: gh}
	commit, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != "" {
		t.Errorf("expected no refusal reason; got %q", reason)
	}
	if commit != "abc1234" {
		t.Errorf("merge commit = %q; want abc1234", commit)
	}
}

func TestMergePRGate_AlreadyMergedIsIdempotent(t *testing.T) {
	gh := fakeGH(map[string]fakeGHResponse{
		"pr view 7 --json labels,mergeable,mergeStateStatus,state": {
			out: []byte(`{"labels":[],"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","state":"MERGED"}`),
		},
	})
	gate := MergePRGate{Strategy: "squash", GH: gh}
	_, reason, err := gate.Merge(context.Background(), 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reason != MergePRReasonAlreadyMerged {
		t.Errorf("expected already_merged reason; got %q", reason)
	}
}

func TestIsValidMergeStrategy(t *testing.T) {
	for _, s := range []string{"squash", "rebase", "merge"} {
		if !IsValidMergeStrategy(s) {
			t.Errorf("expected %q to be valid", s)
		}
	}
	if IsValidMergeStrategy("yolo") {
		t.Error("expected yolo to be invalid")
	}
}
