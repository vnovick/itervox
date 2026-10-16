package linear

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// ProbeViewer is a read-only credential check (CORE-063, `itervox doctor
// --deploy`): one `{ viewer { id name } }` query through the client's normal
// GraphQL transport (same endpoint, auth header and rate-limit handling as
// every other call). It returns the authenticated user's name (or id).
func (c *Client) ProbeViewer(ctx context.Context) (string, error) {
	data, err := c.graphql(ctx, `query ItervoxDoctorProbe { viewer { id name } }`, nil)
	if err != nil {
		return "", err
	}
	payload, _ := data["data"].(map[string]any)
	viewer, _ := payload["viewer"].(map[string]any)
	if viewer == nil {
		return "", decodeError(data)
	}
	if name, _ := viewer["name"].(string); name != "" {
		return name, nil
	}
	if id, _ := viewer["id"].(string); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("linear_probe: viewer has no id")
}

// AssigneeProbe is what ProbeAssigneeScope found (#182, `itervox doctor`).
type AssigneeProbe struct {
	// Teams are the keys of the teams the API key can read.
	Teams []string
	// TeamFound is whether tracker.team (when set) is one of them.
	TeamFound bool
	// Issues counts the issues in the assignee scope the key can read (the
	// first page, up to 50), and IssueTeams the teams they belong to.
	Issues     int
	IssueTeams []string
}

// ProbeAssigneeScope is the read check behind `itervox doctor` for an
// assignee scope (#182): one query listing the teams the key can read and
// the first page of issues assigned to the scope's user (with the team
// filter, when set), so a key that resolves the user but cannot read their
// issues is reported. It resolves the assignee first (cached).
func (c *Client) ProbeAssigneeScope(ctx context.Context) (AssigneeProbe, error) {
	params, filter, vars, err := c.assigneeFilter(ctx)
	if err != nil {
		return AssigneeProbe{}, err
	}
	query := "query ItervoxDoctorAssigneeReads(" + strings.TrimPrefix(params, ", ") + ") {\n" +
		"  teams(first: 100) { nodes { id key } }\n" +
		"  issues(filter: {" + strings.TrimSuffix(filter, ", ") + "}, first: 50) { nodes { team { key } } }\n}"
	body, err := c.graphql(ctx, query, vars)
	if err != nil {
		return AssigneeProbe{}, err
	}
	data, ok := body["data"].(map[string]any)
	if !ok || data == nil {
		return AssigneeProbe{}, decodeError(body)
	}
	var p AssigneeProbe
	team := strings.TrimSpace(c.cfg.Team)
	p.TeamFound = team == ""
	teams, _ := nodesOf(data, "teams")
	for _, t := range teams {
		key := stringValue(t["key"])
		p.Teams = append(p.Teams, key)
		if team != "" && (strings.EqualFold(key, team) || stringValue(t["id"]) == team) {
			p.TeamFound = true
		}
	}
	issues, _ := nodesOf(data, "issues")
	p.Issues = len(issues)
	for _, is := range issues {
		t, _ := is["team"].(map[string]any)
		if key := stringValue(t["key"]); key != "" && !slices.Contains(p.IssueTeams, key) {
			p.IssueTeams = append(p.IssueTeams, key)
		}
	}
	slices.Sort(p.Teams)
	slices.Sort(p.IssueTeams)
	return p, nil
}
