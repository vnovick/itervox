package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactString_Patterns(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		wanted string // substring that MUST appear
		leaked string // substring that MUST NOT appear
	}{
		{
			name:   "anthropic key",
			in:     "called API with sk-ant-api03-XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX-extra",
			wanted: secretMask,
			leaked: "sk-ant-api03-XXXXXXXX",
		},
		{
			name:   "linear api key",
			in:     "header X-Auth: lin_api_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa rest of msg",
			wanted: secretMask,
			leaked: "lin_api_aaaa",
		},
		{
			name:   "github classic PAT",
			in:     "stored ghp_abcdefghijklmnopqrstuvwxyz1234567890",
			wanted: secretMask,
			leaked: "ghp_abcdef",
		},
		{
			name:   "github fine-grained PAT",
			in:     "got github_pat_11AAAAAAA0aaaaaaaaaaaaaa_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa from env",
			wanted: secretMask,
			leaked: "github_pat_11AAAAAAA0",
		},
		{
			name:   "Authorization Bearer header",
			in:     `req: Authorization: Bearer eyJhbGc-some.JWT.TOKEN-here, body=ok`,
			wanted: secretMask,
			leaked: "eyJhbGc-some.JWT.TOKEN-here",
		},
		{
			name:   "no secrets — passthrough",
			in:     "ordinary log message about issue ENG-42",
			wanted: "ENG-42",
			leaked: secretMask, // mask must NOT appear; we have no secret to redact
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactString(tc.in)
			if !strings.Contains(got, tc.wanted) {
				t.Errorf("redactString(%q) = %q; missing wanted substring %q", tc.in, got, tc.wanted)
			}
			if strings.Contains(got, tc.leaked) {
				t.Errorf("redactString(%q) = %q; leaked %q", tc.in, got, tc.leaked)
			}
		})
	}
}

func TestRedactingHandler_RedactsMsgAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewTextHandler(&buf, nil)
	h := NewRedactingHandler(inner)
	log := slog.New(h)

	// Three exfil paths: msg field, plain string attr, and Secret-wrapped attr.
	log.Info(
		"saw token sk-ant-supersecret-1234567890XYZABCD0123456789 in env",
		"raw", "carries lin_api_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa here",
		"wrapped", Secret("not-actually-a-detected-pattern-but-must-still-be-***"),
	)

	out := buf.String()
	if strings.Contains(out, "sk-ant-supersecret") {
		t.Errorf("redacting handler leaked anthropic key in msg: %s", out)
	}
	if strings.Contains(out, "lin_api_aaaa") {
		t.Errorf("redacting handler leaked linear key in attr: %s", out)
	}
	if !strings.Contains(out, "wrapped=***") {
		t.Errorf("expected Secret-wrapped attr to render as '***'; got: %s", out)
	}
}

func TestRedactingHandler_NestedGroup(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewTextHandler(&buf, nil)
	h := NewRedactingHandler(inner)
	log := slog.New(h)

	log.Info("nested test", slog.Group("auth", "header", "Authorization: Bearer ey-secret-jwt-payload"))

	out := buf.String()
	if strings.Contains(out, "ey-secret-jwt-payload") {
		t.Errorf("redacting handler leaked bearer token inside group: %s", out)
	}
}

func TestRedactingHandler_PassesThroughCleanLogs(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewTextHandler(&buf, nil)
	h := NewRedactingHandler(inner)
	log := slog.New(h)

	log.Info("dispatched issue", "identifier", "ENG-42", "turns", 3)

	out := buf.String()
	if !strings.Contains(out, "identifier=ENG-42") {
		t.Errorf("clean log lost expected attr: %s", out)
	}
	if !strings.Contains(out, "turns=3") {
		t.Errorf("clean log lost numeric attr: %s", out)
	}
	if strings.Contains(out, secretMask) {
		t.Errorf("clean log incorrectly emitted mask: %s", out)
	}
}
