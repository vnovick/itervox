package issuefilter

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Parse reads a filter from decoded YAML (or JSON): a list of groups, each
// a map of conditions. A single map is one group. List conditions accept a
// list or a single value. Unknown conditions are an error, as is a value of
// the wrong type, so a typo never silently widens the filter. where names
// the key in messages (e.g. "tracker.filter"). The result is normalized;
// call Validate for the shape rules.
func Parse(raw any, where string) (Filter, error) {
	if raw == nil {
		return nil, nil
	}
	var items []any
	switch v := raw.(type) {
	case []any:
		items = v
	case map[string]any:
		items = []any{v}
	default:
		return nil, fmt.Errorf("%s: expected a list of groups, got %T", where, raw)
	}
	out := make(Filter, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d]: expected a map of conditions, got %T", where, i, item)
		}
		g, err := parseGroup(m, fmt.Sprintf("%s[%d]", where, i))
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out.Normalize(), nil
}

func parseGroup(m map[string]any, at string) (Group, error) {
	var g Group
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := m[k]
		var err error
		switch k {
		case CondProjects:
			g.Projects, err = strList(v)
		case CondTeams:
			g.Teams, err = strList(v)
		case CondAssignee:
			g.Assignee, err = scalar(v)
		case CondLabelsAny:
			g.LabelsAny, err = strList(v)
		case CondLabelsAll:
			g.LabelsAll, err = strList(v)
		case CondLabelsNone:
			g.LabelsNone, err = strList(v)
		case CondPriority:
			g.Priority, err = intList(v)
		case CondStates:
			g.States, err = strList(v)
		case CondCycle:
			g.Cycle, err = scalar(v)
		case CondMilestone:
			g.Milestone, err = scalar(v)
		default:
			return Group{}, fmt.Errorf("%s: unknown condition %q (known: %s)", at, k, strings.Join(AllConditions, ", "))
		}
		if err != nil {
			return Group{}, fmt.Errorf("%s.%s: %w", at, k, err)
		}
	}
	return g, nil
}

func scalar(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return strings.TrimSpace(x), nil
	case int, int64, float64, uint64:
		return strings.TrimSpace(fmt.Sprint(x)), nil
	}
	return "", fmt.Errorf("expected a single value, got %T", v)
}

func strList(v any) ([]string, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s, err := scalar(item)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	case []string:
		return slices.Clone(x), nil
	}
	s, err := scalar(v)
	if err != nil {
		return nil, fmt.Errorf("expected a list, got %T", v)
	}
	return []string{s}, nil
}

func intList(v any) ([]int, error) {
	items, ok := v.([]any)
	if !ok {
		items = []any{v}
	}
	out := make([]int, 0, len(items))
	for _, item := range items {
		switch x := item.(type) {
		case int:
			out = append(out, x)
		case int64:
			out = append(out, int(x))
		case uint64:
			out = append(out, int(x))
		case float64:
			if x != math.Trunc(x) {
				return nil, fmt.Errorf("%v is not a whole number", x)
			}
			out = append(out, int(x))
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(x))
			if err != nil {
				return nil, fmt.Errorf("%q is not a number", x)
			}
			out = append(out, n)
		default:
			return nil, fmt.Errorf("expected numbers, got %T", item)
		}
	}
	return out, nil
}

// YAMLFlow renders the filter as a one-line YAML flow sequence, the form the
// dashboard writes to tracker.filter (one line, so it can be replaced or
// commented out like the other tracker keys).
func (f Filter) YAMLFlow() string {
	groups := make([]string, 0, len(f))
	for _, g := range f {
		var kv []string
		list := func(k string, vs []string) {
			if len(vs) == 0 {
				return
			}
			q := make([]string, len(vs))
			for i, v := range vs {
				q[i] = strconv.Quote(v)
			}
			kv = append(kv, k+": ["+strings.Join(q, ", ")+"]")
		}
		one := func(k, v string) {
			if v != "" {
				kv = append(kv, k+": "+strconv.Quote(v))
			}
		}
		list(CondProjects, g.Projects)
		list(CondTeams, g.Teams)
		one(CondAssignee, g.Assignee)
		list(CondLabelsAny, g.LabelsAny)
		list(CondLabelsAll, g.LabelsAll)
		list(CondLabelsNone, g.LabelsNone)
		if len(g.Priority) > 0 {
			ps := make([]string, len(g.Priority))
			for i, p := range g.Priority {
				ps[i] = strconv.Itoa(p)
			}
			kv = append(kv, CondPriority+": ["+strings.Join(ps, ", ")+"]")
		}
		list(CondStates, g.States)
		one(CondCycle, g.Cycle)
		one(CondMilestone, g.Milestone)
		groups = append(groups, "{"+strings.Join(kv, ", ")+"}")
	}
	return "[" + strings.Join(groups, ", ") + "]"
}
