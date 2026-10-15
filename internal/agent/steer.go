package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

// Steering (#178): operator messages typed into a running session from the
// Sessions panel. Itervox writes only to sessions it owns — the agent CLI
// process it started for this run. There is no TTY injection and no UI
// automation; the static guard TestNoWritesToProcessesItervoxDidNotStart
// keeps it that way.
//
// Two delivery paths, chosen by what the CLI supports:
//
//   - live: a local claude turn started with `--input-format stream-json`
//     ("realtime streaming input", claude --help) reads user messages from
//     its stdin for the whole turn. The prompt is the first message; an
//     operator message is written as a further user message while the turn
//     runs; stdin is closed when the turn's result event arrives, so the CLI
//     finishes whatever it has read and exits. Opt-in (agent.live_steering):
//     a wrapper script that reads stdin to EOF before it starts would never
//     see EOF in this mode.
//   - resume: everywhere else (codex `exec` cannot take input mid-run; SSH
//     workers; live steering off) the message waits for the current turn to
//     end and is delivered as the prompt of the next turn, which resumes the
//     same session (`claude --resume <id> -p`, `codex exec resume <id> -`).

// SteerMessage is one operator message for a running session.
type SteerMessage struct {
	ID   string
	Text string
}

// Steering is a run's operator-message channel. The orchestrator feeds
// Inbox; exactly one side consumes it at a time — the runner while a live
// turn runs (it writes messages into the CLI's stdin), the worker between
// turns (Drain, for delivery on resume).
type Steering struct {
	inbox     <-chan SteerMessage
	live      bool
	delivered func(SteerMessage)

	mu   sync.Mutex
	held []SteerMessage // taken off inbox but not delivered; Drain returns them first
}

// NewSteering returns the steering of one run. live enables writing into a
// local claude turn's stdin; delivered is called after a message was written
// into the live session (from the runner's writer goroutine).
func NewSteering(inbox <-chan SteerMessage, live bool, delivered func(SteerMessage)) *Steering {
	return &Steering{inbox: inbox, live: live, delivered: delivered}
}

// Drain returns every message waiting for delivery — held ones first, then
// whatever the inbox holds right now — without blocking.
func (s *Steering) Drain() []SteerMessage {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	out := s.held
	s.held = nil
	s.mu.Unlock()
	for {
		select {
		case m, ok := <-s.inbox:
			if !ok {
				return out
			}
			out = append(out, m)
		default:
			return out
		}
	}
}

func (s *Steering) hold(m SteerMessage) {
	s.mu.Lock()
	s.held = append(s.held, m)
	s.mu.Unlock()
}

type steeringKey struct{}

// WithSteering attaches a run's steering to the context of its turns.
func WithSteering(ctx context.Context, s *Steering) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, steeringKey{}, s)
}

// steeringFrom returns the steering attached to ctx, or nil.
func steeringFrom(ctx context.Context) *Steering {
	s, _ := ctx.Value(steeringKey{}).(*Steering)
	return s
}

// liveSteeringFor returns the steering a claude turn delivers live, or nil
// when the turn runs the classic way: steering off or absent, an SSH worker
// (its stdin carries the remote script), or a bare resume that reads no
// prompt.
func liveSteeringFor(ctx context.Context, workerHost string, sessionID *string, prompt string) *Steering {
	s := steeringFrom(ctx)
	if s == nil || !s.live || workerHost != "" || !claudeReadsPrompt(sessionID, prompt) {
		return nil
	}
	return s
}

// liveInputFlags switch a claude turn to stream-json input.
var liveInputFlags = []string{"--input-format", "stream-json"}

// streamUserMessage encodes text as one stream-json input line: a user
// message (the shape the CLI's streaming input reads).
func streamUserMessage(text string) ([]byte, error) {
	b, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	})
	if err != nil {
		return nil, fmt.Errorf("agent: encode stream-json message: %w", err)
	}
	return append(b, '\n'), nil
}

// liveInput is the stdin of a live-steered claude turn: a pipe the CLI
// reads and a writer goroutine that owns its write end.
type liveInput struct {
	r, w      *os.File
	stop      chan struct{}
	stopOnce  sync.Once
	closeOnce sync.Once
	done      chan struct{}
}

// newLiveInput creates the pipe. The read end is the CLI's stdin (direct
// exec) or fd 3 (login shell, see localPromptRedirect).
func newLiveInput() (*liveInput, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("agent: live input pipe: %w", err)
	}
	return &liveInput{r: r, w: w, stop: make(chan struct{}), done: make(chan struct{})}, nil
}

// start runs the writer: the prompt first, then every operator message
// that arrives before endInput. Call after the CLI started; the parent's
// copy of the read end is closed here so the CLI's exit makes writes fail
// instead of block.
func (l *liveInput) start(prompt string, s *Steering) {
	_ = l.r.Close()
	go func() {
		defer close(l.done)
		// The writer holds the turn's stdin open: a panic (in the delivery
		// callback, say) must close it, not take the daemon down.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("agent: live steering writer panicked", "panic", r)
			}
		}()
		defer l.closeWriter()
		if err := writeStreamMessage(l.w, prompt); err != nil {
			return
		}
		for {
			select {
			case <-l.stop:
				return
			case m, ok := <-s.inbox:
				if !ok {
					<-l.stop
					return
				}
				if err := writeStreamMessage(l.w, m.Text); err != nil {
					// The CLI is gone: keep the message for the next turn.
					s.hold(m)
					return
				}
				if s.delivered != nil {
					s.delivered(m)
				}
			}
		}
	}()
}

func writeStreamMessage(w io.Writer, text string) error {
	line, err := streamUserMessage(text)
	if err != nil {
		return err
	}
	_, err = w.Write(line)
	return err
}

// endInput closes the CLI's stdin once the writer is done: no further
// operator message is taken for this turn. Idempotent.
func (l *liveInput) endInput() {
	if l == nil {
		return
	}
	l.stopOnce.Do(func() { close(l.stop) })
}

func (l *liveInput) closeWriter() {
	l.closeOnce.Do(func() { _ = l.w.Close() })
}

// finish stops the writer and waits for it. The write end is closed too, so
// a writer blocked on a pipe that a surviving grandchild still holds open
// returns at once.
func (l *liveInput) finish() {
	if l == nil {
		return
	}
	l.endInput()
	l.closeWriter()
	<-l.done
}

// abandon releases a pipe whose CLI never started.
func (l *liveInput) abandon() {
	if l == nil {
		return
	}
	_ = l.r.Close()
	l.closeWriter()
}

// endOnResult wraps a stream parser so the turn's first result event ends
// the live input: the CLI then completes what it has read and exits.
func endOnResult(parse func([]byte) (StreamEvent, error), l *liveInput) func([]byte) (StreamEvent, error) {
	if l == nil {
		return parse
	}
	return func(line []byte) (StreamEvent, error) {
		ev, err := parse(line)
		if err == nil && ev.Type == EventResult {
			l.endInput()
		}
		return ev, err
	}
}
