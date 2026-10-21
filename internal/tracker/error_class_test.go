package tracker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestClassifyErrorNeverEchoesTrackerPayloads pins #117: a sync error is
// shown as a kind plus a summary built from typed fields only. Text an
// adapter put in the error (a GraphQL errors array, a response body) never
// reaches the summary.
func TestClassifyErrorNeverEchoesTrackerPayloads(t *testing.T) {
	const payload = `SECRET-PAYLOAD {"errors":[{"message":"lin_api_abc"}]}`
	reset := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		err  error
		kind string
	}{
		{"nil", nil, ""},
		{"rate limited", fmt.Errorf("pull: %w", &RateLimitedError{Adapter: "linear", ResetAt: reset}), ErrorClassRateLimited},
		{"auth", fmt.Errorf("pull %s: %w", payload, &APIStatusError{Adapter: "github", Status: 401}), ErrorClassAuth},
		{"server", &APIStatusError{Adapter: "linear", Status: 502}, ErrorClassAPIStatus},
		{"not found", &NotFoundError{Adapter: "github", Identifier: "#4"}, ErrorClassNotFound},
		{"graphql", &GraphQLError{Message: payload}, ErrorClassGraphQL},
		{"deadline", fmt.Errorf("%s: %w", payload, context.DeadlineExceeded), ErrorClassTimeout},
		{"canceled", context.Canceled, ErrorClassCanceled},
		{"network", &url.Error{Op: "Post", URL: "https://api.linear.app/graphql?" + payload, Err: &net.OpError{Op: "dial", Err: errors.New("refused")}}, ErrorClassNetwork},
		{"unknown", fmt.Errorf("linear_unknown_payload: %s", payload), ErrorClassUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyError(tc.err)
			assert.Equal(t, tc.kind, got.Kind)
			assert.NotContains(t, got.Summary, "SECRET-PAYLOAD")
			assert.NotContains(t, got.Summary, "lin_api_abc")
			if tc.err != nil {
				assert.NotEmpty(t, got.Summary)
			}
		})
	}
}
