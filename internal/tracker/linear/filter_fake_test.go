package linear_test

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// #198 — the fake Linear server's IssueFilter evaluator. It implements the
// subset of Linear's filter semantics the compiled tracker filter uses
// (and/or, state, project incl. null, team, assignee, labels some/every/
// length, priority, cycle, projectMilestone, updatedAt) and fails the test
// on anything else, so a query the adapter builds wrong returns the wrong
// issues instead of a canned answer. Collection semantics follow Linear's:
// `some` holds when at least one label matches, `every` when no label fails
// (an issue without labels passes), `length` compares the label count.

func evalIssueFilter(t *testing.T, is *fakeIssue, f map[string]any) bool {
	t.Helper()
	for key, raw := range f {
		v, _ := raw.(map[string]any)
		ok := true
		switch key {
		case "and":
			for _, sub := range raw.([]any) {
				if !evalIssueFilter(t, is, sub.(map[string]any)) {
					ok = false
				}
			}
		case "or":
			ok = false
			for _, sub := range raw.([]any) {
				if evalIssueFilter(t, is, sub.(map[string]any)) {
					ok = true
				}
			}
		case "state":
			ok = evalFields(t, v, map[string]any{"name": is.state})
		case "project":
			if null, set := v["null"]; set {
				ok = (is.project == "") == null.(bool)
			} else {
				ok = is.project != "" && evalFields(t, v, map[string]any{"slugId": is.project})
			}
		case "team":
			ok = evalFields(t, v, map[string]any{"id": is.teamID, "key": is.teamKey})
		case "assignee":
			ok = is.assignee != "" && evalFields(t, v, map[string]any{"id": is.assignee})
		case "labels":
			ok = evalLabels(t, is.labels, v)
		case "priority":
			ok = evalComparator(t, float64(is.priority), v)
		case "cycle":
			ok = is.cycle != "" && evalFields(t, v, map[string]any{"id": is.cycle, "isActive": is.cycleActive})
		case "projectMilestone":
			ok = is.milestone != "" && evalFields(t, v, map[string]any{"name": is.milestone})
		case "updatedAt":
			since, err := time.Parse(time.RFC3339Nano, v["gte"].(string))
			if err != nil {
				t.Errorf("updatedAt: %v", err)
			}
			ok = !is.updatedAt.Before(since)
		default:
			t.Errorf("fake Linear: unsupported IssueFilter key %q", key)
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}

// evalFields evaluates {field: comparator, ...} against values.
func evalFields(t *testing.T, f map[string]any, values map[string]any) bool {
	t.Helper()
	for field, cmp := range f {
		val, known := values[field]
		if !known {
			t.Errorf("fake Linear: unsupported field %q", field)
			return false
		}
		if !evalComparator(t, val, cmp.(map[string]any)) {
			return false
		}
	}
	return true
}

func evalComparator(t *testing.T, val any, cmp map[string]any) bool {
	t.Helper()
	for op, want := range cmp {
		var ok bool
		switch op {
		case "eq":
			ok = sameValue(val, want)
		case "neq":
			ok = !sameValue(val, want)
		case "eqIgnoreCase":
			ok = strings.EqualFold(val.(string), want.(string))
		case "in":
			ok = slices.ContainsFunc(want.([]any), func(w any) bool { return sameValue(val, w) })
		case "nin":
			ok = !slices.ContainsFunc(want.([]any), func(w any) bool { return sameValue(val, w) })
		default:
			t.Errorf("fake Linear: unsupported comparator %q", op)
		}
		if !ok {
			return false
		}
	}
	return true
}

func sameValue(a, b any) bool {
	if n, ok := b.(float64); ok {
		if m, ok := a.(float64); ok {
			return m == n
		}
		if m, ok := a.(int); ok {
			return float64(m) == n
		}
	}
	return a == b
}

func evalLabels(t *testing.T, labels []string, f map[string]any) bool {
	t.Helper()
	label := func(name string, lf map[string]any) bool {
		return evalFields(t, lf, map[string]any{"name": name})
	}
	for op, raw := range f {
		var ok bool
		switch op {
		case "some":
			ok = slices.ContainsFunc(labels, func(l string) bool { return label(l, raw.(map[string]any)) })
		case "every":
			ok = !slices.ContainsFunc(labels, func(l string) bool { return !label(l, raw.(map[string]any)) })
		case "length":
			ok = evalComparator(t, float64(len(labels)), raw.(map[string]any))
		default:
			t.Errorf("fake Linear: unsupported label collection filter %q", op)
		}
		if !ok {
			return false
		}
	}
	return true
}
