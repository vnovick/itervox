package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/skills"
	"github.com/vnovick/itervox/internal/workspace"
)

// Repo readiness (#200): the repository-level checks `itervox doctor`
// prints in its "repo readiness" section and GET /api/v1/repo/readiness
// serves to the dashboard's Repo Analysis page. Both build their rows with
// buildRepoReadiness from the results of doctor's own check functions
// (config.Load + ValidateWorkflowSchema, checkBareFetch, checkProfileRefs,
// trackerAPICheck), so there is one implementation.
//
// The endpoint runs on the HTTP request goroutine, never on the
// orchestrator event loop. The only check that leaves the machine — the
// tracker probe — runs in its own goroutine under repoReadinessEnv's
// timeout and is reported as a failure when it does not answer in time,
// whether or not the probe honours its context.

// Readiness check IDs. Stable: the dashboard and tests key on them.
const (
	readinessSchemaVersion = "schema_version"
	readinessGitignore     = "gitignore_carveouts"
	readinessInstructions  = "instruction_docs"
	readinessBareFetch     = "bare_fetch_refspec"
	readinessProfileRefs   = "profile_refs"
	readinessTracker       = "tracker_reachable"
)

// repoReadinessTrackerTimeout bounds the tracker probe.
const repoReadinessTrackerTimeout = 8 * time.Second

// repoReadinessBareFetchTimeout bounds the local git read of the bare
// clone's refspec (no network), the same budget doctor gives it.
const repoReadinessBareFetchTimeout = 10 * time.Second

// agentsGitignoreCarveOuts are the root .gitignore lines that keep
// .itervox/agents/**, .itervox/handoff/** and the project-shared memory
// file (#202) committable under a broad .itervox ignore. `itervox init
// --update` adds them; readiness checks them.
var agentsGitignoreCarveOuts = []string{
	"!.itervox/",
	"!.itervox/agents/",
	"!.itervox/agents/**",
	"!.itervox/handoff/",
	"!.itervox/handoff/**",
	memoryGitignoreCarveOut,
}

// memoryGitignoreCarveOut keeps .itervox/memory.json, the project-shared
// memory (#202), committable.
const memoryGitignoreCarveOut = "!.itervox/memory.json"

// repoReadinessEnv is the injectable seam for the checks that leave the
// machine.
type repoReadinessEnv struct {
	trackerProbe   func(ctx context.Context, cfg *config.Config) deployCheck
	trackerTimeout time.Duration
	now            func() time.Time
}

func defaultRepoReadinessEnv() repoReadinessEnv {
	probe := defaultDeployProbeEnv()
	probe.timeout = repoReadinessTrackerTimeout
	return repoReadinessEnv{
		trackerProbe: func(ctx context.Context, cfg *config.Config) deployCheck {
			return trackerAPICheck(ctx, cfg, probe)
		},
		trackerTimeout: repoReadinessTrackerTimeout,
		now:            time.Now,
	}
}

// doctorReadinessEnv is the env doctor's readiness section uses; a variable
// for tests.
var doctorReadinessEnv = defaultRepoReadinessEnv

// repoReadinessInputs are the results of doctor's check functions that the
// readiness rows report.
type repoReadinessInputs struct {
	cfg       *config.Config
	loadErr   error
	bareFetch BareFetchCheck
	refIssues []skills.InventoryIssue
}

// collectRepoReadiness loads the workflow and runs the readiness checks —
// the endpoint's path. Doctor gets the same inputs from collectDoctorReport.
func collectRepoReadiness(ctx context.Context, workflowPath string, env repoReadinessEnv) server.RepoReadinessResponse {
	cfg, err := config.Load(workflowPath)
	in := repoReadinessInputs{cfg: cfg, loadErr: err}
	if cfg != nil {
		bctx, cancel := context.WithTimeout(ctx, repoReadinessBareFetchTimeout)
		in.bareFetch = checkBareFetch(bctx, cfg, env.now())
		cancel()
		in.refIssues = checkProfileRefs(cfg, workflowPath)
	}
	return server.RepoReadinessResponse{
		Workflow:    workflowPath,
		GeneratedAt: env.now().UTC(),
		Checks:      buildRepoReadiness(ctx, workflowPath, in, env),
	}
}

// repoReadinessProvider is the server's GET /api/v1/repo/readiness provider.
func repoReadinessProvider(workflowPath string) server.RepoReadinessFunc {
	return func(ctx context.Context) server.RepoReadinessResponse {
		return collectRepoReadiness(ctx, workflowPath, defaultRepoReadinessEnv())
	}
}

// buildRepoReadiness turns the check results into readiness rows, in a
// fixed order.
func buildRepoReadiness(ctx context.Context, workflowPath string, in repoReadinessInputs, env repoReadinessEnv) []server.RepoReadinessCheck {
	return []server.RepoReadinessCheck{
		schemaReadiness(workflowPath, in),
		gitignoreReadiness(workflowPath),
		instructionsReadiness(workflowPath, in.cfg),
		bareFetchReadiness(in.cfg, in.bareFetch),
		profileRefsReadiness(in.cfg, in.refIssues),
		trackerReadiness(ctx, in.cfg, env),
	}
}

func readinessRow(id, title, status, detail, fix string) server.RepoReadinessCheck {
	return server.RepoReadinessCheck{ID: id, Title: title, Status: status, Detail: detail, Fix: fix}
}

func schemaReadiness(workflowPath string, in repoReadinessInputs) server.RepoReadinessCheck {
	const title = "WORKFLOW.md schema 2 marker"
	fix := "run `itervox init --update --workflow " + workflowPath + "` to migrate and stamp itervox_schema_version: 2"
	err := in.loadErr
	if err == nil && in.cfg != nil {
		err = config.ValidateWorkflowSchema(in.cfg)
	}
	if err == nil && in.cfg == nil {
		err = errors.New("workflow did not load")
	}
	if err != nil {
		return readinessRow(readinessSchemaVersion, title, server.RepoReadinessFail, firstLine(err.Error()), fix)
	}
	return readinessRow(readinessSchemaVersion, title, server.RepoReadinessPass,
		fmt.Sprintf("itervox_schema_version: %d", in.cfg.SchemaVersion), "")
}

func gitignoreReadiness(workflowPath string) server.RepoReadinessCheck {
	const title = ".gitignore keeps .itervox/agents, .itervox/handoff and .itervox/memory.json committable"
	path := filepath.Join(filepath.Dir(workflowPath), ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		return readinessRow(readinessGitignore, title, server.RepoReadinessPass,
			"no root .gitignore, so nothing under .itervox/ is ignored", "")
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !rootGitignoreHidesAgents(text) {
		return readinessRow(readinessGitignore, title, server.RepoReadinessPass,
			"the root .gitignore does not ignore .itervox/agents or .itervox/handoff", "")
	}
	lines := strings.Split(text, "\n")
	var missing []string
	for _, want := range agentsGitignoreCarveOuts {
		if !containsLine(lines, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		return readinessRow(readinessGitignore, title, server.RepoReadinessPass,
			"carve-outs present for .itervox/agents/**, .itervox/handoff/** and .itervox/memory.json", "")
	}
	return readinessRow(readinessGitignore, title, server.RepoReadinessWarn,
		"the root .gitignore ignores .itervox/ without the carve-outs: "+strings.Join(missing, ", ")+
			" — profile SOUL/INSTRUCTIONS files, agent handoffs or the shared memory are not committed",
		"run `itervox init --update --workflow "+workflowPath+"` (adds the carve-outs), or add the lines yourself")
}

// instructionDocs maps a backend to the repository instruction file it reads.
var instructionDocs = map[string]string{"claude": "CLAUDE.md", "codex": "AGENTS.md"}

func instructionsReadiness(workflowPath string, cfg *config.Config) server.RepoReadinessCheck {
	const title = "CLAUDE.md / AGENTS.md present"
	dir := filepath.Dir(workflowPath)
	var present, missing []string
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			present = append(present, name)
		} else {
			missing = append(missing, name)
		}
	}
	// The files the configured backends read; both when the workflow did
	// not load.
	var needed []string
	if cfg != nil {
		for b, inUse := range backendsInUse(cfg) {
			if inUse && instructionDocs[b] != "" {
				needed = append(needed, instructionDocs[b])
			}
		}
		slices.Sort(needed)
	}
	var lacking []string
	for _, name := range missing {
		if cfg == nil || slices.Contains(needed, name) {
			lacking = append(lacking, name)
		}
	}
	switch {
	case cfg == nil && len(present) > 0:
		return readinessRow(readinessInstructions, title, server.RepoReadinessPass, "found "+strings.Join(present, ", "), "")
	case len(lacking) == 0:
		detail := "found " + strings.Join(present, ", ")
		if len(present) == 0 {
			detail = "no backend in use reads a repository instruction file"
		}
		return readinessRow(readinessInstructions, title, server.RepoReadinessPass, detail, "")
	default:
		return readinessRow(readinessInstructions, title, server.RepoReadinessWarn,
			"missing "+strings.Join(lacking, ", ")+" — agents start without the repository's conventions",
			"add "+strings.Join(lacking, " and ")+" at the repository root (CLAUDE.md is read by Claude Code, AGENTS.md by Codex)")
	}
}

func bareFetchReadiness(cfg *config.Config, c BareFetchCheck) server.RepoReadinessCheck {
	const title = "Shared bare clone fetch refspec"
	switch {
	case cfg == nil:
		return readinessRow(readinessBareFetch, title, server.RepoReadinessWarn, "not checked: the workflow did not load", "fix the schema check first")
	case !c.Ran:
		return readinessRow(readinessBareFetch, title, server.RepoReadinessPass, "no shared bare clone (workspace.worktree with clone_url is off)", "")
	case c.Missing:
		return readinessRow(readinessBareFetch, title, server.RepoReadinessPass, c.Path+" not cloned yet — created on the first dispatch", "")
	case c.Failing:
		return readinessRow(readinessBareFetch, title, server.RepoReadinessFail,
			"the last fetch of "+c.Path+" failed: "+or(c.Status.LastError, "unknown error"),
			"fix the fetch (credentials, network, remote) — new worktrees branch from a stale base until a fetch succeeds")
	case c.LegacyRefspec:
		return readinessRow(readinessBareFetch, title, server.RepoReadinessWarn,
			c.Path+" still fetches with "+workspace.LegacyBareFetchRefspec,
			"no action needed: the daemon migrates it to "+workspace.BareFetchRefspec+" on the next worktree it creates")
	case c.Stale:
		return readinessRow(readinessBareFetch, title, server.RepoReadinessWarn,
			fmt.Sprintf("%s was last fetched %s ago", c.Path, c.Age),
			"the next worktree the daemon creates fetches again")
	default:
		return readinessRow(readinessBareFetch, title, server.RepoReadinessPass, c.Path+" uses "+workspace.BareFetchRefspec, "")
	}
}

func profileRefsReadiness(cfg *config.Config, issues []skills.InventoryIssue) server.RepoReadinessCheck {
	const title = "Skill and subagent references resolve"
	if cfg == nil {
		return readinessRow(readinessProfileRefs, title, server.RepoReadinessWarn, "not checked: the workflow did not load", "fix the schema check first")
	}
	var warns, infos []string
	for _, i := range issues {
		if i.Severity == "info" {
			infos = append(infos, i.Title)
		} else {
			warns = append(warns, i.Title)
		}
	}
	if len(warns) > 0 {
		return readinessRow(readinessProfileRefs, title, server.RepoReadinessWarn, strings.Join(warns, "; "),
			"fix the name in SOUL.md / INSTRUCTIONS.md, or add the skill under .claude/skills/<name>/SKILL.md or the subagent as .claude/agents/<name>.md")
	}
	if len(infos) > 0 {
		return readinessRow(readinessProfileRefs, title, server.RepoReadinessPass, "all resolve; note: "+strings.Join(infos, "; "), "")
	}
	return readinessRow(readinessProfileRefs, title, server.RepoReadinessPass, "every profile reference resolves", "")
}

// trackerReadiness runs the tracker probe in its own goroutine and gives up
// after env.trackerTimeout, so a hung tracker can only cost this request
// that long.
func trackerReadiness(ctx context.Context, cfg *config.Config, env repoReadinessEnv) server.RepoReadinessCheck {
	const title = "Tracker reachable"
	if cfg == nil {
		return readinessRow(readinessTracker, title, server.RepoReadinessWarn, "not checked: the workflow did not load", "fix the schema check first")
	}
	cctx, cancel := context.WithTimeout(ctx, env.trackerTimeout)
	defer cancel()
	done := make(chan deployCheck, 1)
	go func() {
		defer orchestrator.RecoverGoroutine("repo-readiness-tracker", "", nil)
		done <- env.trackerProbe(cctx, cfg)
	}()
	var d deployCheck
	select {
	case d = <-done:
	case <-cctx.Done():
		return readinessRow(readinessTracker, title, server.RepoReadinessFail,
			fmt.Sprintf("the %s tracker did not answer within %s", cfg.Tracker.Kind, env.trackerTimeout),
			"check the network and the tracker's status, then re-run; `itervox doctor --deploy` probes it with more detail")
	}
	switch d.Status {
	case deployOK:
		return readinessRow(readinessTracker, title, server.RepoReadinessPass, d.Detail, "")
	case deploySkipped:
		return readinessRow(readinessTracker, title, server.RepoReadinessPass, d.Detail, "")
	case deployWarn:
		return readinessRow(readinessTracker, title, server.RepoReadinessWarn, d.Detail, "grant the token write access to the repository")
	default:
		return readinessRow(readinessTracker, title, server.RepoReadinessFail, d.Detail,
			"check tracker.api_key (and .itervox/.env), tracker.endpoint and the network; `itervox doctor --deploy` probes it with more detail")
	}
}

// renderRepoReadiness writes doctor's "repo readiness" section: one line per
// check, with the fix for anything that is not a pass.
func renderRepoReadiness(b *strings.Builder, checks []server.RepoReadinessCheck) {
	if len(checks) == 0 {
		return
	}
	b.WriteString("repo readiness:\n")
	for _, c := range checks {
		fmt.Fprintf(b, "  [%s] %s: %s", c.Status, c.ID, c.Title)
		if c.Detail != "" {
			fmt.Fprintf(b, " — %s", c.Detail)
		}
		b.WriteString("\n")
		if c.Status != server.RepoReadinessPass && c.Fix != "" {
			fmt.Fprintf(b, "         fix: %s\n", c.Fix)
		}
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
