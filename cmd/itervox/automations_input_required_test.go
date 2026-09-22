package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vnovick/itervox/internal/orchestrator"
)

func TestInputRequiredReplayKeyPrefersCommentKey(t *testing.T) {
	entry := &orchestrator.InputRequiredEntry{
		QuestionCommentKey: "11111111-1111-4111-8111-111111111111",
		QuestionCommentID:  "legacy-id",
	}
	assert.Equal(t, "comment-key:11111111-1111-4111-8111-111111111111", inputRequiredReplayKey(entry))

	legacy := &orchestrator.InputRequiredEntry{QuestionCommentID: "legacy-id"}
	assert.Equal(t, "comment:legacy-id", inputRequiredReplayKey(legacy), "legacy entries keep their identity")
}
