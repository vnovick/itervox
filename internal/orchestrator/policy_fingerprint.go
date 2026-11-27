package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/playbook"
	"github.com/vnovick/itervox/internal/rules"
)

// Policy fingerprint (approval integrity): what governed a run when it
// started — the files that govern agents (WORKFLOW.md, the settings and
// rules layers, the playbooks, the profiles' SOUL.md / INSTRUCTIONS.md, the
// referee policy) and the effective config of the run's profile that limits
// it (push, isolation, permission mode, allowed actions, command,
// guardrails and lanes). Hashes only: never a file's text or a config value.
//
// startRun records it on the run's handle (event loop, like the rules and
// playbook files it already reads there); the worker recomputes it before
// it sends its exit (deliverExit, off the loop) and the exit carries the
// names that differ (RunEntry.PolicyChanged), which the exit handler puts on
// the run's CompletedRun, logs at Warn and reports as a policy_changed feed
// event. PromptParts already identifies the prompt content the run was
// given; the fingerprint hashes the files on disk, so an edit made after
// the run read them still shows.

// policyKeyPrefix marks a config key among the changed names
// ("config:agent.push"), so it is never mistaken for a file.
const policyKeyPrefix = "config:"

// policyAbsent is the hash recorded for a governing file that does not exist.
const policyAbsent = "absent"

// Bounds on one fingerprint: governing directories are small, but a
// fingerprint must never turn into a walk of an arbitrary tree.
const (
	policyMaxFiles     = 512
	policyMaxFileBytes = 4 << 20
	policyHashLen      = 16
)

// policyFingerprint is a run's policy at one moment: name → short hash.
// Files are named relative to WORKFLOW.md's directory ("~/" for the user
// layers); keys are config paths ("config:agent.push").
type policyFingerprint struct {
	files map[string]string
	keys  map[string]string
}

// empty reports a fingerprint that recorded nothing (no workflow path).
func (f policyFingerprint) empty() bool { return f.files == nil && f.keys == nil }

// policySource is one governing file, or a directory of them.
type policySource struct {
	name, path string
	dir        bool
}

// policySources lists the files that govern a run of profile. cfg fields
// read here are load-time, except Profiles (cfgMu; the caller holds it).
func policySources(cfg *config.Config, profile string) []policySource {
	wf := cfg.WorkflowPath
	if wf == "" {
		return nil
	}
	base := filepath.Dir(wf)
	name := func(p string) string { return policyFileName(base, p) }
	var src []policySource
	add := func(p string, dir bool) {
		if p == "" || slices.ContainsFunc(src, func(s policySource) bool { return s.path == p }) {
			return
		}
		src = append(src, policySource{name: name(p), path: p, dir: dir})
	}
	add(wf, false)
	add(config.ProjectSettingsPath(wf), false)
	add(config.LocalSettingsPath(wf), false)
	for _, f := range cfg.SettingsFiles {
		add(f.Path, false)
	}
	add(rules.UserPath(), false)
	add(rules.ProjectPath(wf), false)
	add(rules.LocalPath(wf), false)
	add(filepath.Join(base, ".itervox", "agents"), true)
	add(playbook.Dir(cfg), true)
	if p, ok := cfg.Agent.Profiles[profile]; ok {
		for _, f := range []string{p.SoulFile, p.InstructionsFile} {
			if f != "" && !filepath.IsAbs(f) {
				f = filepath.Join(base, f)
			}
			add(f, false)
		}
	}
	add(filepath.Join(base, ".referee.yml"), false)
	return src
}

// policyFileName names path for the fingerprint and the warnings: relative
// to the project directory when inside it, "~/…" under $HOME, else as is.
func policyFileName(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

// hashPolicySources hashes every source: a missing file is policyAbsent; a
// directory contributes one entry per regular file in it (a missing
// directory none), bounded by policyMaxFiles.
func hashPolicySources(src []policySource) map[string]string {
	out := map[string]string{}
	for _, s := range src {
		if !s.dir {
			out[s.name] = hashPolicyFile(s.path)
			continue
		}
		_ = filepath.WalkDir(s.path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // a missing or unreadable directory hashes as no files
			}
			if len(out) >= policyMaxFiles {
				return filepath.SkipAll
			}
			if d.Type().IsRegular() {
				rel, _ := filepath.Rel(s.path, p)
				out[s.name+"/"+filepath.ToSlash(rel)] = hashPolicyFile(p)
			}
			return nil
		})
	}
	return out
}

// hashPolicyFile is the short SHA-256 of a file's first policyMaxFileBytes.
func hashPolicyFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return policyAbsent
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, policyMaxFileBytes)); err != nil {
		return "unreadable"
	}
	return hex.EncodeToString(h.Sum(nil))[:policyHashLen]
}

// shortJSONHash is the short SHA-256 of v's JSON form.
func shortJSONHash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:policyHashLen]
}

// policyKeys hashes the effective config that limits a run of profile.
// The caller holds cfgMu for reading (Profiles is a cfgMu field).
func policyKeys(cfg *config.Config, profile string) map[string]string {
	p := cfg.Agent.Profiles[profile]
	keys := map[string]any{
		"agent.push":            config.ResolvePush(p.Push, cfg.Agent.Push),
		"agent.isolation":       []any{config.ResolveIsolation(p.Isolation, cfg.Agent.Isolation), p.IsolationExtraWrite, p.IsolationExtraDeny},
		"agent.permission_mode": p.PermissionMode,
		"agent.allowed_actions": p.AllowedActions,
		"agent.command":         []string{p.Command, cfg.Agent.Command},
		"guardrails": []any{cfg.Guardrails.Enabled, cfg.Guardrails.AllowForcePush,
			cfg.Guardrails.ApprovalCommands, cfg.Guardrails.EnvApprovalCommands},
		"guardrails.lanes": cfg.Guardrails.Lanes,
	}
	out := make(map[string]string, len(keys))
	for k, v := range keys {
		out[policyKeyPrefix+k] = shortJSONHash(v)
	}
	return out
}

// runPolicyFingerprint is the fingerprint of a run of profile now. Safe
// from the event loop and from a worker (it takes cfgMu for reading).
func (o *Orchestrator) runPolicyFingerprint(profile string) policyFingerprint {
	if o == nil || o.cfg == nil || o.cfg.WorkflowPath == "" {
		return policyFingerprint{}
	}
	o.cfgMu.RLock()
	src := policySources(o.cfg, profile)
	keys := policyKeys(o.cfg, profile)
	o.cfgMu.RUnlock()
	return policyFingerprint{files: hashPolicySources(src), keys: keys}
}

// policyChanges returns the names whose hash differs between two
// fingerprints (a file present in only one counts), sorted.
func policyChanges(was, now policyFingerprint) []string {
	var out []string
	diff := func(a, b map[string]string) {
		for k, v := range a {
			if b[k] != v && !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	diff(was.files, now.files)
	diff(now.files, was.files)
	diff(was.keys, now.keys)
	diff(now.keys, was.keys)
	slices.Sort(out)
	return out
}

// policyFileChanges is policyChanges over the files only.
func policyFileChanges(was, now map[string]string) []string {
	return policyChanges(policyFingerprint{files: was}, policyFingerprint{files: now})
}

// cloneFiles copies a fingerprint's file map.
func cloneFiles(m map[string]string) map[string]string { return maps.Clone(m) }

// runPolicyChanged recomputes the run's fingerprint and returns what
// changed since it started (nil when nothing did, or when the run recorded
// none). Worker goroutine (deliverExit).
func (o *Orchestrator) runPolicyChanged(h *runHandle) []string {
	if h == nil || h.policy.empty() {
		return nil
	}
	return policyChanges(h.policy, o.runPolicyFingerprint(h.profile))
}
