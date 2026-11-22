package referee

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/guardrails"
)

// defaultPolicyPath is the policy file, read from the base ref.
const defaultPolicyPath = ".referee.yml"

// policyFile is the policy file's shape: two lists of gitignore-style
// globs. A key that is absent takes its default; an empty list turns the
// lane off. JSON is valid YAML, so a JSON policy parses too.
type policyFile struct {
	Human         *[]string `yaml:"human"`
	SecondOpinion *[]string `yaml:"second_opinion"`
}

// policy is the lanes a diff is classified with, and where they came from.
type policy struct {
	Lanes guardrails.Lanes
	// Source is "<path>@<base commit>" or "default (<path> not on the base ref)".
	Source string
}

// parsePolicy parses a policy file. Unknown keys are an error, so a typo
// (`humans:`) cannot silently fall back to the defaults.
func parsePolicy(raw []byte) (guardrails.Lanes, error) {
	var f policyFile
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return guardrails.Lanes{}, fmt.Errorf("parse policy: %w", err)
	}
	lanes := guardrails.Lanes{Human: slices.Clone(config.DefaultGuardrailHumanLane)}
	if f.Human != nil {
		lanes.Human = cleanPatterns(*f.Human)
	}
	if f.SecondOpinion != nil {
		lanes.SecondOpinion = cleanPatterns(*f.SecondOpinion)
	}
	for _, lane := range []struct {
		name string
		list []string
	}{{"human", lanes.Human}, {"second_opinion", lanes.SecondOpinion}} {
		for _, p := range lane.list {
			if err := validatePattern(p); err != nil {
				return guardrails.Lanes{}, fmt.Errorf("policy %s: %w", lane.name, err)
			}
		}
	}
	return lanes, nil
}

func cleanPatterns(raw []string) []string {
	out := []string{}
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// validatePattern rejects a pattern that can never match (a malformed `[`
// class, or a bare `!`): a pattern that silently matches nothing would let
// the files it names through.
func validatePattern(p string) error {
	body := strings.Trim(strings.TrimPrefix(p, "!"), "/")
	if body == "" {
		return fmt.Errorf("pattern %q names no path", p)
	}
	for _, seg := range strings.Split(strings.ReplaceAll(body, `\`, "/"), "/") {
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("invalid pattern %q: %w", p, err)
		}
	}
	return nil
}

// loadPolicy reads the policy from the BASE commit, never from the working
// tree or the head: the branch under review must not be able to rewrite
// the rules it is judged by. A base without the file uses the defaults.
// The policy file itself is always in the human lane, so a change to it is
// seen by a human before it takes effect.
func loadPolicy(ctx context.Context, dir, baseCommit, policyPath string) (policy, error) {
	policyPath = normalizePolicyPath(policyPath)
	if policyPath == "" {
		return policy{}, errors.New("--policy names no file")
	}
	raw, found, err := showFile(ctx, dir, baseCommit, policyPath)
	if err != nil {
		return policy{}, fmt.Errorf("read %s from the base ref: %w", policyPath, err)
	}
	var lanes guardrails.Lanes
	source := fmt.Sprintf("default (%s not on the base ref)", policyPath)
	if found {
		if lanes, err = parsePolicy(raw); err != nil {
			return policy{}, fmt.Errorf("%s at the base ref: %w", policyPath, err)
		}
		source = policyPath + "@" + baseCommit[:min(len(baseCommit), 12)]
	} else {
		lanes, _ = parsePolicy(nil)
	}
	// Last, so no `!` pattern of the policy can exclude the policy itself.
	lanes.Human = append(lanes.Human, "/"+policyPath)
	return policy{Lanes: lanes, Source: source}, nil
}
