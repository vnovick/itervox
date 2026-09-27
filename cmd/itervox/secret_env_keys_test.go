package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// CORE-104: the dotenv INFO line names the agent credentials too.
func TestSecretEnvKeysIncludeOpenAIAndOAuthToken(t *testing.T) {
	for _, k := range []string{"ITERVOX_API_TOKEN", "LINEAR_API_KEY", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		assert.Contains(t, secretEnvKeys, k)
	}
}
