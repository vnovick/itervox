package evals

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/domain"
)

// RequiredCheck is one required CI check on a scenario PR.
type RequiredCheck struct {
	Name   string `yaml:"name"`
	Status string `yaml:"status"` // passing | failing | pending
}

// PRCandidate is one open PR a scenario's `gh pr list` returns.
type PRCandidate struct {
	Number int    `yaml:"number"`
	Head   string `yaml:"head"`
}

// rawInput mirrors input.yaml. Unknown keys are ignored: input.yaml also
// carries free-form commentary for the human reading the fixture.
type rawInput struct {
	Issue struct {
		Identifier string `yaml:"identifier"`
		State      string `yaml:"state"`
		Title      string `yaml:"title"`
		Body       string `yaml:"body"`
		BranchName string `yaml:"branch_name"`
	} `yaml:"issue"`
	Comment struct {
		Body   string `yaml:"body"`
		Author string `yaml:"author"`
	} `yaml:"comment"`
	PR struct {
		Number         int             `yaml:"number"`
		Labels         []string        `yaml:"labels"`
		RequiredChecks []RequiredCheck `yaml:"required_checks"`
		Candidates     *[]PRCandidate  `yaml:"candidates"`
		Diff           string          `yaml:"diff"`
	} `yaml:"pr"`
}

// ParseInput reads a scenario's input.yaml into a ScenarioInput. Live mode
// feeds it to the agent: the issue (and triggering comment) through the
// composed prompt, the PR state through the sandbox's `gh` shim.
func ParseInput(dir string) (ScenarioInput, error) {
	data, err := os.ReadFile(filepath.Join(dir, "input.yaml"))
	if err != nil {
		return ScenarioInput{}, err
	}
	var raw rawInput
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return ScenarioInput{}, fmt.Errorf("evals: %s/input.yaml: %w", dir, err)
	}
	in := ScenarioInput{
		IssueIdentifier: raw.Issue.Identifier,
		IssueState:      raw.Issue.State,
		IssueTitle:      raw.Issue.Title,
		IssueBody:       raw.Issue.Body,
		BranchName:      raw.Issue.BranchName,
		CommentBody:     raw.Comment.Body,
		CommentAuthor:   raw.Comment.Author,
		PRNumber:        raw.PR.Number,
		PRLabels:        raw.PR.Labels,
		RequiredChecks:  raw.PR.RequiredChecks,
		PRDiff:          raw.PR.Diff,
	}
	if raw.PR.Candidates != nil {
		in.Candidates = *raw.PR.Candidates
		in.HasCandidates = true
	}
	return in, nil
}

// Issue converts the input to the domain.Issue the prompt renders. The
// triggering comment, when present, is the issue's latest comment — the
// same place a tracker_comment_added dispatch finds it.
func (in ScenarioInput) Issue() domain.Issue {
	issue := domain.Issue{
		ID:         "eval-" + in.IssueIdentifier,
		Identifier: in.IssueIdentifier,
		Title:      in.IssueTitle,
		State:      in.IssueState,
	}
	if in.IssueBody != "" {
		body := in.IssueBody
		issue.Description = &body
	}
	if in.BranchName != "" {
		branch := in.BranchName
		issue.BranchName = &branch
	}
	if in.CommentBody != "" {
		created := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
		issue.Comments = []domain.Comment{{
			ID:         "eval-comment-1",
			Body:       in.CommentBody,
			CreatedAt:  &created,
			AuthorID:   "eval-operator",
			AuthorName: in.commentAuthor(),
		}}
	}
	return issue
}

func (in ScenarioInput) commentAuthor() string {
	if in.CommentAuthor != "" {
		return in.CommentAuthor
	}
	return "operator"
}

// sandboxPR is one open PR as the `gh` shim reports it.
type sandboxPR struct {
	Number int
	Head   string
	Labels []string
	Checks []RequiredCheck
	Diff   string
}

// pullRequests lists the open PRs the scenario sets up: `pr.candidates`
// when given (an empty list means "no open PR"), else the single `pr.number`
// on the issue branch. Checks default to one passing ci/test.
func (in ScenarioInput) pullRequests() []sandboxPR {
	checks := in.RequiredChecks
	if len(checks) == 0 {
		checks = []RequiredCheck{{Name: "ci/test", Status: "passing"}}
	}
	var out []sandboxPR
	if in.HasCandidates {
		for _, c := range in.Candidates {
			out = append(out, sandboxPR{Number: c.Number, Head: c.Head, Labels: in.PRLabels, Checks: checks, Diff: in.PRDiff})
		}
		return out
	}
	if in.PRNumber > 0 {
		out = append(out, sandboxPR{Number: in.PRNumber, Head: in.BranchName, Labels: in.PRLabels, Checks: checks, Diff: in.PRDiff})
	}
	return out
}
