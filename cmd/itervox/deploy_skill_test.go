package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", rel))
	require.NoError(t, err, rel)
	return string(data)
}

// TestDeploySkillReferencesExist (#88): the deploy skill's front matter
// parses as strict YAML, and every deploy-kit path, provision flag and
// itervox command it tells an agent to run exists.
func TestDeploySkillReferencesExist(t *testing.T) {
	skill := repoFile(t, ".claude/skills/deploy/SKILL.md")
	require.True(t, strings.HasPrefix(skill, "---\n"))
	front := skill[4 : 4+strings.Index(skill[4:], "\n---\n")]
	var meta struct{ Name, Description string }
	require.NoError(t, yaml.Unmarshal([]byte(front), &meta))
	assert.Equal(t, "deploy", meta.Name)
	assert.NotEmpty(t, meta.Description)

	for _, m := range regexp.MustCompile("`?(deploy/[A-Za-z0-9_./-]+)").FindAllStringSubmatch(skill, -1) {
		p := strings.TrimRight(m[1], "./")
		p = strings.TrimSuffix(p, "/<gcp-vm|aws-ec2|azure-vm>/examples/basic")
		if strings.Contains(p, "<") {
			continue
		}
		_, err := os.Stat(filepath.Join("..", "..", p))
		assert.NoError(t, err, "the skill names %s", p)
	}
	for _, mod := range []string{"gcp-vm", "aws-ec2", "azure-vm"} {
		_, err := os.Stat(filepath.Join("..", "..", "deploy", "terraform", mod, "examples", "basic"))
		assert.NoError(t, err, mod)
	}

	flagRe := regexp.MustCompile(`(--[a-z][a-z-]*)`)
	for _, cloud := range []string{"gcp", "aws", "azure"} {
		script := repoFile(t, "deploy/"+cloud+"/provision.sh")
		row := regexp.MustCompile("`\\./deploy/" + cloud + "/provision\\.sh([^`]*)`").FindStringSubmatch(skill)
		require.NotNil(t, row, cloud)
		for _, f := range flagRe.FindAllString(row[1], -1) {
			assert.Contains(t, script, f+")", "%s provision.sh has no %s", cloud, f)
		}
	}
	bootstrap := repoFile(t, "deploy/bootstrap.sh")
	for _, f := range []string{"--version", "--public", "--repo", "--dry-run", "--data-disk"} {
		assert.Contains(t, skill, f)
		assert.Contains(t, bootstrap, f+")", "bootstrap.sh has no %s", f)
	}

	// Every module input the OpenTofu table names exists in that module.
	identRe := regexp.MustCompile("`([a-z]+(?:_[a-z]+)+)`")
	for _, mod := range []string{"gcp-vm", "aws-ec2", "azure-vm"} {
		row := regexp.MustCompile("(?m)^\\| `" + mod + "` \\|.*$").FindString(skill)
		require.NotEmpty(t, row, mod)
		vars := repoFile(t, "deploy/terraform/"+mod+"/variables.tf") + repoFile(t, "deploy/terraform/"+mod+"/examples/basic/main.tf")
		for _, m := range identRe.FindAllStringSubmatch(row, -1) {
			assert.Contains(t, vars, m[1], "%s has no input %s", mod, m[1])
		}
	}

	main := repoFile(t, "cmd/itervox/main.go")
	for _, cmd := range []string{"secret", "doctor"} {
		assert.Contains(t, main, `case "`+cmd+`":`)
	}
	assert.Contains(t, repoFile(t, "cmd/itervox/doctor_deploy.go"), `"deploy: OK\n"`, "the skill quotes doctor's success line")
}

// TestDeployDocsPinTheLatestRelease (#88): every `--version vX.Y.Z` example
// in the deploy docs names the newest release in CHANGELOG.md, so the
// examples cannot fall behind a release again.
func TestDeployDocsPinTheLatestRelease(t *testing.T) {
	m := regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`).FindStringSubmatch(repoFile(t, "CHANGELOG.md"))
	require.NotNil(t, m)
	latest := "v" + m[1]
	versionRe := regexp.MustCompile(`--version (v\d+\.\d+\.\d+)`)
	found := 0
	for _, doc := range []string{"deploy/README.md", "site/src/content/docs/guides/deployment.mdx", "deploy/upgrade.sh", "docs/deploy-runbook.md"} {
		for _, v := range versionRe.FindAllStringSubmatch(repoFile(t, doc), -1) {
			found++
			assert.Equal(t, latest, v[1], "%s pins %s; the latest release is %s", doc, v[1], latest)
		}
	}
	assert.Positive(t, found)
}
