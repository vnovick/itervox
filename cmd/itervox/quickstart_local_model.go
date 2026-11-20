package main

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/workflow"
)

// The optional local-model step of `itervox quickstart`: add a `local`
// profile whose agent talks to a model server on this machine
// (agent.profiles.local.model_server). Interactive runs ask; --model-server
// adds it without asking; --yes alone and --no-local-model skip it.

// quickstartLocalProfile is the name of the profile the step writes.
const quickstartLocalProfile = "local"

// quickstartDefaultModelServer is the URL offered by default: port 8000 on
// the loopback address, which several open-source servers with
// OpenAI- and Anthropic-compatible APIs listen on by default and which the
// local-models guide uses.
const quickstartDefaultModelServer = "http://127.0.0.1:8000"

// quickstartLocalModel runs the step after the doctor checks passed, so the
// workflow loads. Returns the exit code (0 when skipped).
func quickstartLocalModel(workflowPath string, opts quickstartOptions, in *bufio.Reader, out io.Writer) int {
	if opts.NoLocalModel {
		return 0
	}
	cfg, err := config.Load(workflowPath)
	if err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	if _, exists := cfg.Agent.Profiles[quickstartLocalProfile]; exists {
		if opts.ModelServer != "" {
			_, _ = fmt.Fprintf(out, "itervox quickstart: a %q profile already exists; left it unchanged\n", quickstartLocalProfile)
		}
		return 0
	}
	url := strings.TrimSpace(opts.ModelServer)
	if url == "" {
		if opts.Yes {
			return 0 // --yes answers confirmations; a local model is opt-in
		}
		if !askYesNo(in, out, "Use a local model server for a `local` profile?") {
			return 0
		}
		url = askLine(in, out, "Model server URL", quickstartDefaultModelServer)
	}
	if err := config.CheckModelServerValue(url); err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: model server %q: %v\n", url, err)
		return 1
	}
	profile := config.AgentProfile{
		Command:     config.BackendBinary(quickstartLocalBackend(cfg.Agent.Command)),
		ModelServer: url,
	}
	if err := config.ApplyProfileModelServer(quickstartLocalProfile, &profile, cfg.Agent.Command); err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	if probeErr := probeModelServer(profile.ModelServerURL); probeErr != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %s is not answering (%v)\n", profile.ModelServerURL, probeErr)
		if opts.ModelServer == "" && !askYesNo(in, out, "Add the `local` profile anyway?") {
			return 0
		}
	} else {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %s is answering\n", profile.ModelServerURL)
	}
	profile, err = ensureSchema2ProfileFiles(workflowPath, quickstartLocalProfile, profile, true, true)
	if err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	profiles := make(map[string]config.AgentProfile, len(cfg.Agent.Profiles)+1)
	maps.Copy(profiles, cfg.Agent.Profiles)
	profiles[quickstartLocalProfile] = profile
	if err := workflow.ApplyAndWriteFrontMatter(workflowPath, workflow.MutateProfilesBlock(profilesToEntries(profiles))); err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	if _, err := config.Load(workflowPath); err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: the workflow does not load with the `local` profile: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "itervox quickstart: added the `local` profile (%s, model_server %s, pool %s)\n",
		profile.Command, profile.ModelServer, profile.Pool)
	_, _ = fmt.Fprintf(out, "  Pass the model your server serves in its command (--model <name>) on the Agents page or in %s.\n", workflowPath)
	return 0
}

// quickstartLocalBackend is the backend of the local profile: agent.command's
// when it is one model_server supports (claude or codex), else claude.
func quickstartLocalBackend(defaultCommand string) string {
	backend := config.BackendFromCommand(defaultCommand)
	if config.ModelServerEnvVar(backend) == "" {
		return config.BackendClaude
	}
	return backend
}

// askYesNo asks question with a default of no; unlike confirmPrompt, --yes
// does not answer it.
func askYesNo(in *bufio.Reader, out io.Writer, question string) bool {
	return confirmPrompt(in, out, false, question)
}

// askLine asks for a value, returning def for an empty answer.
func askLine(in *bufio.Reader, out io.Writer, question, def string) string {
	_, _ = fmt.Fprintf(out, "%s [%s]: ", question, def)
	line, _ := in.ReadString('\n')
	if v := strings.TrimSpace(line); v != "" {
		return v
	}
	_, _ = fmt.Fprintln(out)
	return def
}
