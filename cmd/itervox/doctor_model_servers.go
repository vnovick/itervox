package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
)

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

func checkModelServers(cfg *config.Config) ModelServerCheck {
	if cfg == nil {
		return ModelServerCheck{}
	}
	var c ModelServerCheck
	for _, use := range config.ModelServerUses(defaultProfileLabel, cfg.Agent.Command, cfg.Agent.Profiles) {
		s := ModelServer{Profiles: use.Profiles, Var: use.Var, URL: use.URL, Unchecked: use.Unchecked}
		if !s.Unchecked {
			if u, err := url.Parse(use.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				s.Err = errors.New("not an http(s) URL")
			} else {
				s.Err = probeModelServer(use.URL)
			}
		}
		c.Servers = append(c.Servers, s)
	}
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
