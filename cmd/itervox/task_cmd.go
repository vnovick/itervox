package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/vnovick/itervox/internal/server"
)

// `itervox task` (#251): create a New task on the running daemon, from the
// terminal. The daemon creates it (a local task file by default, or a
// tracker issue with --tracker) and dispatches it through its normal
// admission path; --start asks for that at once.
//
//	itervox task "<title>" [--instructions <text|@file>] [--profile NAME]
//	    [--backend claude|codex] [--playbook NAME] [--base BRANCH]
//	    [--tracker] [--start] [--template NAME] [--var key=value ...]
//	itervox task --templates

const taskUsage = `usage: itervox task "<title>" [--instructions <text|@file>] [--profile NAME] [--backend claude|codex] [--playbook NAME] [--base BRANCH] [--tracker] [--start] [--template NAME] [--var key=value ...]
       itervox task --templates`

func runTask(args []string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fatalExit(taskCommand(ctx, args, os.Stdout, os.Stderr))
}

// varFlags collects repeated --var key=value flags.
type varFlags map[string]string

func (v varFlags) String() string { return "" }

func (v varFlags) Set(s string) error {
	key, value, ok := strings.Cut(s, "=")
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return fmt.Errorf("--var wants key=value, got %q", s)
	}
	v[key] = value
	return nil
}

// taskFlags are the flags of `itervox task` besides the daemon's.
type taskFlags struct {
	instructions, profile, backend, playbook, base, template string
	tracker, start, templates                                bool
	vars                                                     varFlags
}

func (f *taskFlags) register(fs *flag.FlagSet) {
	f.vars = varFlags{}
	fs.StringVar(&f.instructions, "instructions", "", "custom instructions (Markdown) for this task; @path reads them from a file, @- from stdin")
	fs.StringVar(&f.profile, "profile", "", "the agent profile to run the task with")
	fs.StringVar(&f.backend, "backend", "", "the backend to run the task on: claude or codex")
	fs.StringVar(&f.playbook, "playbook", "", "the playbook to attach (a name, or none)")
	fs.StringVar(&f.base, "base", "", "the branch the task's worktree starts from and its pull request targets")
	fs.BoolVar(&f.tracker, "tracker", false, "create the task as an issue in the project's tracker instead of a local task")
	fs.BoolVar(&f.start, "start", false, "dispatch the task at once (it still waits for a free slot, guardrails, budgets and dependencies)")
	fs.StringVar(&f.template, "template", "", "start from the saved template .itervox/tasks/NAME.md")
	fs.Var(f.vars, "var", "a template placeholder value, key=value (repeatable)")
	fs.BoolVar(&f.templates, "templates", false, "list the saved task templates and exit")
}

// taskRequest builds the POST /api/v1/tasks body from the flags and the
// positional title. stdin is read for --instructions @-.
func (f *taskFlags) taskRequest(pos []string, stdin io.Reader) (server.TaskCreateRequest, error) {
	if len(pos) > 1 {
		return server.TaskCreateRequest{}, errors.New("give the title as one argument (quote it)")
	}
	req := server.TaskCreateRequest{
		Profile: f.profile, Backend: f.backend, Playbook: f.playbook, BaseBranch: f.base,
		Template: f.template, Start: f.start, Where: server.TaskWhereLocal,
	}
	if len(pos) == 1 {
		req.Title = strings.TrimSpace(pos[0])
	}
	if req.Title == "" && req.Template == "" {
		return req, errors.New("a task needs a title (or --template)")
	}
	if f.tracker {
		req.Where = server.TaskWhereTracker
	}
	if len(f.vars) > 0 {
		if req.Template == "" {
			return req, errors.New("--var fills a template's placeholders; pass --template too")
		}
		req.Vars = map[string]string(f.vars)
	}
	text, err := readInstructions(f.instructions, stdin)
	if err != nil {
		return req, err
	}
	req.Instructions = text
	return req, nil
}

// readInstructions returns v, the file @path names, or stdin for @-.
func readInstructions(v string, stdin io.Reader) (string, error) {
	path, isFile := strings.CutPrefix(v, "@")
	if !isFile {
		return v, nil
	}
	if path == "-" {
		raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if err != nil {
			return "", fmt.Errorf("read the instructions from stdin: %w", err)
		}
		return string(raw), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the instructions: %w", err)
	}
	return string(raw), nil
}

// taskCommand runs `itervox task` and returns its exit code: 0 created, 1
// the daemon refused or could not be reached, 2 usage.
func taskCommand(ctx context.Context, args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("task", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var df daemonFlags
	df.register(fs)
	var tf taskFlags
	tf.register(fs)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintln(errOut, "itervox task: "+err.Error())
		return 1
	}
	var req server.TaskCreateRequest
	if !tf.templates {
		if req, err = tf.taskRequest(pos, os.Stdin); err != nil {
			_, _ = fmt.Fprintln(errOut, "itervox task: "+err.Error()+"\n"+taskUsage)
			return 2
		}
	}
	c, err := df.client()
	if err != nil {
		return fail(err)
	}
	rctx, cancel := context.WithTimeout(ctx, clientTimeout)
	defer cancel()
	if tf.templates {
		var list server.TaskTemplatesResponse
		if err := c.request(rctx, http.MethodGet, "/task-templates", nil, &list); err != nil {
			return fail(err)
		}
		printTaskTemplates(out, list)
		return 0
	}
	var res server.TaskCreateResult
	if err := c.request(rctx, http.MethodPost, "/tasks", req, &res); err != nil {
		return fail(err)
	}
	where := "a local task"
	if !res.Local {
		where = "a tracker issue"
	}
	_, _ = fmt.Fprintf(out, "Created %s (%s) in %s: %s\n", res.Identifier, where, res.State, res.Title)
	if res.Started {
		_, _ = fmt.Fprintln(out, "Dispatch requested; it starts when a slot is free.")
	} else {
		_, _ = fmt.Fprintln(out, "It is picked up by the next poll.")
	}
	return 0
}

func printTaskTemplates(out io.Writer, list server.TaskTemplatesResponse) {
	if len(list.Templates) == 0 {
		_, _ = fmt.Fprintln(out, "No task templates (save one from the New task form, or add .itervox/tasks/<name>.md).")
	}
	for _, t := range list.Templates {
		line := t.Name + " — " + t.Title
		if len(t.Vars) > 0 {
			line += " (vars: " + strings.Join(t.Vars, ", ") + ")"
		}
		_, _ = fmt.Fprintln(out, line)
	}
	for _, p := range list.Problems {
		_, _ = fmt.Fprintf(out, "WARNING: %s: %s\n", p.Path, p.Message)
	}
}
