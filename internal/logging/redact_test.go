package logging

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
		{
			name:   "dashboard URL token query param (?token=)",
			in:     "url: http://localhost:8090/?token=abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567 done",
			wanted: "?token=" + secretMask,
			leaked: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567",
		},
		{
			name:   "dashboard URL token query param (&token=)",
			in:     "callback?foo=bar&token=abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567&rest=1",
			wanted: "&token=" + secretMask,
			leaked: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567",
		},
		{
			name:   "short hex token value is NOT matched (below 32-char floor)",
			in:     "url: http://localhost:8090/?token=deadbeef0123 rest",
			wanted: "?token=deadbeef0123",
			leaked: secretMask,
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

func TestRegisterSecret_RedactsExactValueInMsgAndAttrs(t *testing.T) {
	value := "not-a-pattern-match-but-a-registered-raw-token-9f3c7a1e"
	RegisterSecret(value)

	if got := redactString("env dump: ITERVOX_API_TOKEN=" + value); strings.Contains(got, value) {
		t.Errorf("redactString leaked registered value in msg-shaped string: %s", got)
	}

	var buf bytes.Buffer
	h := NewRedactingHandler(slog.NewTextHandler(&buf, nil))
	log := slog.New(h)
	log.Info("subprocess env", "raw_env_dump", "ITERVOX_API_TOKEN="+value)

	out := buf.String()
	if strings.Contains(out, value) {
		t.Errorf("RedactingHandler leaked registered secret value in attr: %s", out)
	}
	if !strings.Contains(out, secretMask) {
		t.Errorf("expected registered value to be replaced with mask; got: %s", out)
	}
}

func TestRegisterSecret_EmptyAndShortValuesNoOp(t *testing.T) {
	// Neither call should register — both are no-ops per the <8-char /
	// empty-value guard. If either registered, the literal substrings below
	// would be redacted to secretMask; assert they are NOT.
	RegisterSecret("")
	RegisterSecret("short12") // 7 chars — below the 8-char floor

	msg := "value seen: short12 and nothing else"
	if got := redactString(msg); got != msg {
		t.Errorf("redactString(%q) = %q; short/empty RegisterSecret call was not a no-op", msg, got)
	}
}

func TestRegisterSecret_ConcurrentWithLogging(t *testing.T) {
	// The daemon logs from many goroutines (worker subprocesses, HTTP
	// handlers, the orchestrator event loop) while RegisterSecret may be
	// called concurrently at startup. Exercise both under -race.
	var buf syncBuffer
	h := NewRedactingHandler(slog.NewTextHandler(&buf, nil))
	log := slog.New(h)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			RegisterSecret(fmt.Sprintf("concurrent-secret-value-%d-abcdefgh", i))
		}(i)
		go func(i int) {
			defer wg.Done()
			log.Info("worker tick", "n", i, "msg", fmt.Sprintf("iteration %d ordinary text", i))
		}(i)
	}
	wg.Wait()
}

// syncBuffer wraps bytes.Buffer with a mutex so concurrent slog.Handler
// writes in TestRegisterSecret_ConcurrentWithLogging don't themselves race
// on the io.Writer — that would be a race in the test fixture, not in the
// code under test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
