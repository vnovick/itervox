package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
)

// modelServerEnvVars are the variables an agent CLI reads to talk to a model
// server other than its vendor's: a local engine such as colibri, or a
// proxy. Only assignments in the profile command itself are seen; a CLI's
// own config file (Codex's config.toml, OpenCode's providers) is not read.
var modelServerEnvVars = []string{"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE"}

// modelServerProbeTimeout bounds each probe.
const modelServerProbeTimeout = 3 * time.Second

// ModelServer is one model server a profile command points its agent at.
type ModelServer struct {
	// Profiles are the run modes using it ("(default)" for agent.command).
	Profiles []string
	Var      string
	URL      string
	// Err is why the server did not answer; nil when it did.
	Err error
	// Unchecked is set when the URL comes from a shell variable ($VAR),
	// which the agent's shell expands at run time.
	Unchecked bool
}

// ModelServerCheck is doctor's "Model servers" section.
type ModelServerCheck struct {
	Servers []ModelServer
}

// probeModelServer reports whether anything answers HTTP at rawURL. Any
// HTTP response counts: the check is "is the server up", not "is this
// endpoint right", since engines differ in what they serve at the base.
// Tests swap it.
var probeModelServer = func(rawURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), modelServerProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// modelServerURLs returns the model-server variables assigned in command
// before the program, e.g. `ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude`.
func modelServerURLs(command string) map[string]string {
	out := map[string]string{}
	for _, token := range strings.Fields(command) {
		if token == "env" || strings.HasPrefix(token, "-") {
			continue
		}
		if !config.IsEnvAssignment(token) {
			break
		}
		key, val, _ := strings.Cut(token, "=")
		val = strings.Trim(val, `"'`)
		for _, v := range modelServerEnvVars {
			if key == v && val != "" {
				out[key] = val
			}
		}
	}
	return out
}

func checkModelServers(cfg *config.Config) ModelServerCheck {
	if cfg == nil {
		return ModelServerCheck{}
	}
	type key struct{ v, u string }
	users := map[key][]string{}
	add := func(name, command string) {
		for v, u := range modelServerURLs(command) {
			users[key{v, u}] = append(users[key{v, u}], name)
		}
	}
	add(defaultProfileLabel, cfg.Agent.Command)
	for name, p := range cfg.Agent.Profiles {
		if !config.ProfileEnabled(p) {
			continue
		}
		command := p.Command
		if strings.TrimSpace(command) == "" {
			command = cfg.Agent.Command
		}
		add(name, command)
	}
	var c ModelServerCheck
	for k, names := range users {
		sort.Strings(names)
		s := ModelServer{Profiles: names, Var: k.v, URL: k.u}
		if strings.Contains(k.u, "$") {
			s.Unchecked = true
		} else if u, err := url.Parse(k.u); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			s.Err = fmt.Errorf("not an http(s) URL")
		} else {
			s.Err = probeModelServer(k.u)
		}
		c.Servers = append(c.Servers, s)
	}
	sort.Slice(c.Servers, func(i, j int) bool {
		if c.Servers[i].URL != c.Servers[j].URL {
			return c.Servers[i].URL < c.Servers[j].URL
		}
		return c.Servers[i].Var < c.Servers[j].Var
	})
	return c
}

// renderModelServerCheck prints the section only when a profile points at
// a model server. A server that does not answer warns: every run of those
// profiles would fail, but the server may simply not be started yet, so
// the exit code is unchanged.
func renderModelServerCheck(b *strings.Builder, c ModelServerCheck) {
	if len(c.Servers) == 0 {
		return
	}
	b.WriteString("\nModel servers\n")
	for _, s := range c.Servers {
		who := strings.Join(s.Profiles, ", ")
		if s.Unchecked {
			fmt.Fprintf(b, "  %s=%s, used by %s: set from a variable, not checked\n", s.Var, s.URL, who)
			continue
		}
		if s.Err == nil {
			fmt.Fprintf(b, "  %s=%s, used by %s: answering\n", s.Var, s.URL, who)
			continue
		}
		fmt.Fprintf(b, "  %s=%s, used by %s: not answering\n", s.Var, s.URL, who)
		fmt.Fprintf(b, "WARNING: %s point their agent at %s, which did not answer (%v); their runs will fail until it does — start the model server (for colibri: `coli serve`) or fix %s\n",
			who, s.URL, s.Err, s.Var)
	}
}
