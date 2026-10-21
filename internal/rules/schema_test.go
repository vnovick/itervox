package rules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPublishedRulesSchemaMatchesParser (#157): the JSON Schema published at
// SchemaURL (site/public/schema/rules.schema.json) describes exactly what
// Parse accepts: the same fields, required fields, severities and id pattern.
func TestPublishedRulesSchemaMatchesParser(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "site", "public", "schema", "rules.schema.json"))
	require.NoError(t, err)
	var schema struct {
		ID         string                     `json:"$id"`
		Properties map[string]json.RawMessage `json:"properties"`
		Defs       struct {
			Rule struct {
				Required   []string `json:"required"`
				Properties map[string]struct {
					Enum       []string                   `json:"enum"`
					Pattern    string                     `json:"pattern"`
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"properties"`
			} `json:"rule"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	assert.Equal(t, SchemaURL, schema.ID)
	assert.ElementsMatch(t, topKeys, keys(schema.Properties))
	rule := schema.Defs.Rule
	assert.ElementsMatch(t, ruleKeys, keys(rule.Properties))
	assert.ElementsMatch(t, []string{"id", "severity", "text"}, rule.Required)
	assert.ElementsMatch(t, []string{string(SeverityMust), string(SeverityShould)}, rule.Properties["severity"].Enum)
	assert.Equal(t, idRe.String(), rule.Properties["id"].Pattern)
	assert.ElementsMatch(t, scopeKeys, keys(rule.Properties["scope"].Properties))
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
