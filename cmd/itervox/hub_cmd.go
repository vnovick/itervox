package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/vnovick/itervox/internal/hub"
)

// `itervox hub` (#228) prints the machine's hub URL and its members. It
// reads the daemon registry and the hub lock directly, so it needs no
// token and prints none.
//
//	itervox hub [--json]
//	itervox hub slack setup|status   (#230, hub_slack.go)

// hubStatus is the `itervox hub --json` shape.
type hubStatus struct {
	// Hub is the serving hub's lock record, null when no hub runs.
	Hub     *hub.LockInfo     `json:"hub"`
	Members []hubStatusMember `json:"members"`
}

type hubStatusMember struct {
	Name     string `json:"name"`
	Workflow string `json:"workflow"`
	URL      string `json:"url"`
	PID      int    `json:"pid"`
	Version  string `json:"version"`
}

func runHub(args []string) {
	if len(args) > 0 && args[0] == "slack" { // #230
		fatalExit(hubSlackCmd(args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	fatalExit(hubCmd(args, os.Stdout, os.Stderr))
}

// hubCmd runs `itervox hub` and returns the exit code.
func hubCmd(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	fs.SetOutput(errOut)
	asJSON := fs.Bool("json", false, "print JSON: {\"hub\": <hub.lock record or null>, \"members\": [...]}")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(errOut, "usage: itervox hub [--json]\n       itervox hub slack setup|status\n\nPrints the machine's itervox hub URL and its member projects.\nHub tokens: itervox token create --hub --name NAME --scope viewer|operator|admin")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	dir, err := daemonRegistryDir()
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox hub: %v\n", err)
		return 1
	}
	st := hubStatusIn(dir)
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(st)
		return 0
	}
	if st.Hub == nil {
		_, _ = fmt.Fprintln(out, "no hub is running: the first daemon started (without --no-hub) becomes the hub")
	} else {
		_, _ = fmt.Fprintf(out, "hub: %s (pid %d, itervox %s)\n", st.Hub.URL, st.Hub.PID, st.Hub.Version)
		_, _ = fmt.Fprintln(out, "open it with a hub token: itervox token create --hub --name NAME --scope viewer|operator|admin")
	}
	if len(st.Members) == 0 {
		_, _ = fmt.Fprintln(out, "members: none")
		return 0
	}
	_, _ = fmt.Fprintf(out, "members (%d):\n", len(st.Members))
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	for _, m := range st.Members {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", m.Name, m.URL, m.Workflow)
	}
	_ = tw.Flush()
	return 0
}

// hubStatusIn reads the hub lock and the members from the registry dir.
func hubStatusIn(dir string) hubStatus {
	st := hubStatus{Members: []hubStatusMember{}}
	if info, ok, _ := hub.ReadLockInfo(dir); ok {
		st.Hub = &info
	}
	members := hubMembersIn(dir)
	names := hub.ProjectNames(members)
	for _, m := range members {
		st.Members = append(st.Members, hubStatusMember{Name: names[m.Workflow], Workflow: m.Workflow, URL: m.URL, PID: m.PID, Version: m.Version})
	}
	return st
}
