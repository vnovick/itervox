package issuefilter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/domain"
)

func prio(p int) *int { return &p }

// TestMatchSemantics (#198): the in-memory matcher — AND inside a group, OR
// between groups, any/all/none labels (any case), priority, teams by key or
// id, cycle, milestone, the no-project sentinel, the states condition
// narrowing only active states, and unresolved assignees reported unknown.
func TestMatchSemantics(t *testing.T) {
	env := Env{
		Assignee:     func(spec string) (string, bool) { return map[string]string{"me": "u1"}[spec], spec == "me" },
		ActiveStates: []string{"Todo", "In Progress"}, WorkingState: "In Progress",
	}
	is := domain.Issue{
		State: "Todo", Labels: []string{"agent", "backend"}, Priority: prio(1), ProjectSlug: "eng",
		TeamID: "t1", TeamKey: "ENG", Assignees: []string{"u1"}, CycleID: "c1", CycleActive: true, Milestone: "Beta",
	}
	cases := []struct {
		name  string
		f     Filter
		match bool
	}{
		{"empty filter", nil, true},
		{"AND holds", Filter{{Projects: []string{"eng"}, LabelsAny: []string{"AGENT"}}}, true},
		{"AND fails on one condition", Filter{{Projects: []string{"eng"}, LabelsAny: []string{"ai"}}}, false},
		{"OR: second group", Filter{{Projects: []string{"ops"}}, {Assignee: "me"}}, true},
		{"labels_all extra labels fine", Filter{{LabelsAll: []string{"agent"}}}, true},
		{"labels_all missing one", Filter{{LabelsAll: []string{"agent", "bug"}}}, false},
		{"labels_none", Filter{{LabelsNone: []string{"backend"}}}, false},
		{"priority", Filter{{Priority: []int{2, 3}}}, false},
		{"team by key", Filter{{Teams: []string{"eng"}}}, true},
		{"team by id", Filter{{Teams: []string{"t1"}}}, true},
		{"current cycle", Filter{{Cycle: CycleCurrent}}, true},
		{"cycle id", Filter{{Cycle: "c2"}}, false},
		{"milestone any case", Filter{{Milestone: "beta"}}, true},
		{"no project", Filter{{Projects: []string{NoProject}}}, false},
		{"states narrow", Filter{{States: []string{"Backlog"}}}, false},
		{"states exclude another active state", Filter{{States: []string{"In Progress"}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, known := tc.f.Match(is, env)
			assert.True(t, known)
			assert.Equal(t, tc.match, m)
		})
	}
	moved := is
	moved.State = "In Progress"
	m, _ := Filter{{States: []string{"Todo"}}}.Match(moved, env)
	assert.True(t, m, "a dispatched issue in the working state stays in scope")
	moved.State = "Done"
	m, _ = Filter{{States: []string{"Todo"}}}.Match(moved, env)
	assert.True(t, m, "states narrow only the active states")

	m, known := Filter{{Assignee: "ada@example.com"}}.Match(is, env)
	assert.False(t, m)
	assert.False(t, known, "an unresolved assignee is not a definitive no")
	m, known = Filter{{Assignee: "ada@example.com"}, {Projects: []string{"eng"}}}.Match(is, env)
	assert.True(t, m)
	assert.True(t, known)
}

// TestParseLabelKeyAndFlow (#198): parsing from YAML, the people-facing
// label, a key independent of order and case, and the one-line flow form
// the dashboard writes parsing back to the same filter.
func TestParseLabelKeyAndFlow(t *testing.T) {
	var raw any
	require.NoError(t, yaml.Unmarshal([]byte(`
- projects: [ENG]
  labels_any: [agent, ai]
  labels_none: [blocked]
- assignee: me
  labels_all: [urgent]
  priority: [1, 2]
  cycle: current
  milestone: "Q4 \"launch\""
`), &raw))
	f, err := Parse(raw, "tracker.filter")
	require.NoError(t, err)
	assert.Equal(t, `ENG · agent|ai · ¬blocked  OR  me · urgent · P1,P2 · cycle current · milestone Q4 "launch"`, f.Label())
	require.NoError(t, f.Validate("tracker.filter"))

	swapped := Filter{f[1], {Projects: []string{"ENG"}, LabelsAny: []string{"AI", "agent"}, LabelsNone: []string{"Blocked"}}}
	assert.Equal(t, f.Key(), swapped.Key(), "order and label case do not change the key")
	assert.NotEqual(t, f.Key(), f[:1].Key())

	var back any
	require.NoError(t, yaml.Unmarshal([]byte(f.YAMLFlow()), &back))
	again, err := Parse(back, "tracker.filter")
	require.NoError(t, err)
	assert.Equal(t, f, again)

	_, err = Parse("agent", "tracker.filter")
	require.ErrorContains(t, err, "expected a list of groups")
	_, err = Parse([]any{map[string]any{"projects": map[string]any{}}}, "tracker.filter")
	require.ErrorContains(t, err, "tracker.filter[0].projects")
	assert.Equal(t, "priority", Filter{{Priority: []int{1}}}.Unsupported(SupportedBy("github")))
	assert.Equal(t, "", Filter{{LabelsAll: []string{"a"}}}.Unsupported(SupportedBy("github")))
}
