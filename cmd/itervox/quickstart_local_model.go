package main

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/workflow"
)

// The optional local-model step of `itervox quickstart`: add a `local`
// profile whose agent talks to a model server on this machine
// (agent.profiles.local.model_server). No port is assumed. An interactive
// run lists the model servers that answer on the usual ports of common
// local servers (quickstart_detect_models.go) and the operator picks one or
// types a port, host:port or URL; with none detected it asks whether to use
// one and for its port, with no default. --model-server URL (with an
// explicit port) adds it without asking; --yes alone and --no-local-model
// skip the step.

// quickstartLocalProfile is the name of the profile the step writes.
const quickstartLocalProfile = "local"

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
	backend := quickstartLocalBackend(cfg.Agent.Command)
	url := strings.TrimSpace(opts.ModelServer)
	var detected []detectedModelServer
	if url == "" {
		if opts.Yes {
			return 0 // --yes answers confirmations; a local model is opt-in
		}
		detected = detectModelServers(quickstartModelServerCandidates)
		if url = askModelServer(in, out, detected, backend); url == "" {
			return 0
		}
	}
	if err := config.CheckModelServerValue(url); err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: model server %q: %v\n", url, err)
		return 1
	}
	profile := config.AgentProfile{
		Command:     config.BackendBinary(backend),
		ModelServer: url,
	}
	if err := config.ApplyProfileModelServer(quickstartLocalProfile, &profile, cfg.Agent.Command); err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	served := detectedModels(detected, profile.ModelServerURL)
	if served == nil {
		if probeErr := probeModelServer(profile.ModelServerURL); probeErr != nil {
			_, _ = fmt.Fprintf(out, "itervox quickstart: %s is not answering (%v)\n", profile.ModelServerURL, probeErr)
			if opts.ModelServer == "" && !askYesNo(in, out, "Add the `local` profile anyway?") {
				return 0
			}
		} else {
			_, _ = fmt.Fprintf(out, "itervox quickstart: %s is answering\n", profile.ModelServerURL)
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
	if err := config.AssignModelServerPools(profiles); err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	if err := workflow.ApplyAndWriteFrontMatter(workflowPath, workflow.MutateProfilesBlock(profilesToEntries(profiles))); err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %v\n", err)
		return 1
	}
	loaded, err := config.Load(workflowPath)
	if err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: the workflow does not load with the `local` profile: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "itervox quickstart: added the `local` profile (%s, model_server %s, pool %s)\n",
		profile.Command, profile.ModelServer, loaded.Agent.Profiles[quickstartLocalProfile].Pool)
	hint := "--model <name>"
	if len(served) > 0 {
		hint = "--model " + served[0]
	}
	_, _ = fmt.Fprintf(out, "  Pass the model your server serves in its command (%s) on the Agents page or in %s.\n", hint, workflowPath)
	return 0
}

// askModelServer asks which model server the `local` profile uses: one of
// detected, or a port, host:port or URL the operator types. "" skips the
// step (an empty answer, a "no", or the end of input). A malformed answer
// is asked again.
func askModelServer(in *bufio.Reader, out io.Writer, detected []detectedModelServer, backend string) string {
	question := "Model server port, host:port or URL (empty skips)"
	if len(detected) > 0 {
		_, _ = fmt.Fprintln(out, "Model servers answering on this machine:")
		for i, d := range detected {
			_, _ = fmt.Fprintf(out, "  %d) %s\n", i+1, d.describe())
		}
		question = "Add a `local` profile on one? Enter its number, or another port, host:port or URL (empty skips)"
	} else {
		ports := make([]string, 0, len(quickstartModelServerCandidates))
		for _, c := range quickstartModelServerCandidates {
			ports = append(ports, strconv.Itoa(c.Port)+" "+c.Server)
		}
		if len(ports) > 0 {
			_, _ = fmt.Fprintf(out, "No local model server answered on the usual ports (%s).\n", strings.Join(ports, ", "))
		}
		if !askYesNo(in, out, "Use a local model server for a `local` profile?") {
			return ""
		}
	}
	for {
		_, _ = fmt.Fprintf(out, "%s: ", question)
		line, readErr := in.ReadString('\n')
		url, err := modelServerFromAnswer(line, detected, backend)
		if err == nil && url != "" {
			err = config.CheckModelServerValue(url)
		}
		if err == nil {
			if url == "" {
				_, _ = fmt.Fprintln(out)
			}
			return url
		}
		_, _ = fmt.Fprintf(out, "\n  %v\n", err)
		if readErr != nil {
			return ""
		}
	}
}

// detectedModels returns the models of the detected server url is on, or
// nil when url is not one of them (it was typed, so it is probed).
func detectedModels(detected []detectedModelServer, url string) []string {
	key := config.ModelServerKey(url)
	for _, d := range detected {
		if config.ModelServerKey(d.URL) == key {
			if d.Models == nil {
				return []string{}
			}
			return d.Models
		}
	}
	return nil
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
