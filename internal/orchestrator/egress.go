package orchestrator

import (
	"context"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/egress"
	"github.com/vnovick/itervox/internal/gitexec"
)

// runEgressInputs builds the egress of one run (nil when the profile runs
// with egress off). Runs on the worker goroutine and touches no State:
// cfg.Agent.Egress* are read-only after startup, agentActionBaseURL is set
// before Run, and the workspace remotes are read with gitexec.
func (o *Orchestrator) runEgressInputs(ctx context.Context, profile config.AgentProfile, backend, command, wsPath, identifier, runLogID string) *agent.EgressInputs {
	in := agent.EgressInputsFor(config.ResolveEgress(profile, o.cfg.Agent, backend, command, config.EnvModelServerURLs()), identifier)
	if in == nil {
		return nil
	}
	in.Deny = workspaceRemoteHosts(ctx, wsPath)
	// The guardrail shims and agent actions call the daemon's API: bridged
	// in as a local port when it listens on loopback, else allowed through
	// the proxy by its exact host and port.
	if host, port, ok := daemonAPIAddr(o.agentActionBaseURL); ok {
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			if !slices.Contains(in.LocalPorts, port) {
				in.LocalPorts = append(in.LocalPorts, port)
			}
		} else {
			in.Allow = append(in.Allow, net.JoinHostPort(host, strconv.Itoa(port)))
		}
	}
	in.OnDecision = o.egressRefusalReporter(identifier, runIDFrom(ctx), runLogID)
	return in
}

// egressRefusalReporter returns the proxy's decision callback for one run:
// each refused destination is written to the run log and recorded once as
// an egress_refused feed event (a non-blocking send; a dropped event is
// counted). Allowed connections are only logged by the proxy itself.
func (o *Orchestrator) egressRefusalReporter(identifier, runID, runLogID string) func(egress.Decision) {
	var mu sync.Mutex
	seen := map[string]bool{}
	return func(d egress.Decision) {
		if d.Allowed {
			return
		}
		target := d.Target()
		mu.Lock()
		first := !seen[target]
		seen[target] = true
		mu.Unlock()
		if !first {
			return
		}
		if o.logBuf != nil {
			o.logBuf.Add(identifier, makeBufLineWithSession("WARN",
				"worker: egress refused: "+target+" ("+d.Reason+"); add it to agent.egress_allow to allow it", runLogID))
		}
		o.RecordFeedEvent(FeedEvent{Kind: FeedEgressRefused, Identifier: identifier,
			Payload: FeedPayload{RunID: runID, Kind: d.Reason, Reason: target}})
	}
}

// daemonAPIAddr splits the daemon base URL into host and port.
func daemonAPIAddr(base string) (string, int, bool) {
	if base == "" {
		return "", 0, false
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return "", 0, false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return "", 0, false
	}
	return u.Hostname(), port, true
}

// workspaceRemoteHosts returns the hosts of the workspace's git remotes
// (fetch and push URLs): the proxy refuses them even when an allow entry
// matches, so git traffic stays with the daemon.
func workspaceRemoteHosts(ctx context.Context, wsPath string) []string {
	if wsPath == "" {
		return nil
	}
	out, err := gitexec.Command(ctx, wsPath, "config", "--get-regexp", `^remote\..*\.(push)?url$`).Output()
	if err != nil {
		return nil
	}
	var hosts []string
	for line := range strings.SplitSeq(string(out), "\n") {
		_, val, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if h := egress.HostOfRemote(val); h != "" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// logEgressPushAgent notes, once per run, that an egress run with push:
// agent cannot push: the proxy refuses the push remote and the
// credentials are hidden. push: daemon is the combination that works.
func (o *Orchestrator) logEgressPushAgent(identifier, runLogID string, profile config.AgentProfile) {
	if config.ResolvePush(profile.Push, o.cfg.Agent.Push) == config.PushDaemon {
		return
	}
	const msg = "worker: egress allowlist with push: agent: the agent cannot reach its push remote; set push: daemon so Itervox pushes after the run"
	slog.Warn(msg, "issue_identifier", identifier)
	if o.logBuf != nil {
		o.logBuf.Add(identifier, makeBufLineWithSession("WARN", msg, runLogID))
	}
}
