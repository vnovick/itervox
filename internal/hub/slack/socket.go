package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/vnovick/itervox/internal/hub"
)

// Socket Mode: the hub's single connection to the Slack app. apps.
// connections.open (app token) returns a WebSocket URL; Slack sends a
// hello, then envelopes the client acknowledges by envelope_id within
// three seconds, and a disconnect message before it closes a connection
// (to refresh it, or because Socket Mode was turned off). The client
// reconnects with doubling backoff, and at once after a disconnect
// warning. Ping frames are answered by the WebSocket library.

// maxFrameBytes bounds one Socket Mode frame.
const maxFrameBytes = 1 << 20

// envelope is one Socket Mode message.
type envelope struct {
	Type       string          `json:"type"`
	EnvelopeID string          `json:"envelope_id"`
	Payload    json.RawMessage `json:"payload"`
	Reason     string          `json:"reason"`
}

// errDisconnect ends a connection Slack asked us to leave.
type errDisconnect struct{ reason string }

func (e errDisconnect) Error() string { return "slack: Socket Mode disconnect: " + e.reason }

// healthyAfter is how long a connection must have lasted for the backoff
// to start over.
const healthyAfter = 30 * time.Second

// Run implements hub.InteractiveChannel: it holds the Socket Mode
// connection until ctx ends, reconnecting with backoff.
func (c *Channel) Run(ctx context.Context, members hub.MemberActions) {
	c.members.Store(&members)
	backoff := c.cfg.ReconnectMin
	for ctx.Err() == nil {
		start := time.Now()
		err := c.session(ctx)
		c.connected.Store(false)
		if ctx.Err() != nil {
			return
		}
		var dis errDisconnect
		switch {
		case errors.As(err, &dis) && dis.reason != "link_disabled":
			// Slack is refreshing the connection: reconnect at once.
			c.log.Info("hub: slack: Socket Mode connection refreshed", "reason", dis.reason)
			backoff = c.cfg.ReconnectMin
			continue
		case time.Since(start) > healthyAfter:
			backoff = c.cfg.ReconnectMin
		}
		if permanent(err) || errors.As(err, &dis) {
			// A revoked token or Socket Mode turned off: no point hammering.
			backoff = c.cfg.ReconnectMax
		}
		wait := backoff/2 + rand.N(backoff/2+1) // jitter
		c.log.Warn("hub: slack: Socket Mode connection lost; reconnecting", "retry_in", wait.Round(time.Millisecond).String(), "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(2*backoff, c.cfg.ReconnectMax)
	}
}

// session runs one connection until it fails, Slack disconnects it or ctx
// ends.
func (c *Channel) session(ctx context.Context) error {
	wsURL, err := c.openConnection(ctx)
	if err != nil {
		return err
	}
	cfg, err := websocket.NewConfig(wsURL, "https://localhost/")
	if err != nil {
		return errors.New("slack: bad Socket Mode URL")
	}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	ws, err := cfg.DialContext(dialCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("slack: Socket Mode dial: %w", scrubURLError(err))
	}
	ws.MaxPayloadBytes = maxFrameBytes
	c.connections.Add(1)
	stop := context.AfterFunc(ctx, func() { _ = ws.Close() })
	defer stop()
	defer func() { _ = ws.Close() }()

	var writeMu sync.Mutex
	ack := func(id string) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		data, _ := json.Marshal(map[string]string{"envelope_id": id})
		_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return websocket.Message.Send(ws, string(data))
	}
	for {
		_ = ws.SetReadDeadline(time.Now().Add(c.cfg.ReadIdle))
		var frame []byte
		if err := websocket.Message.Receive(ws, &frame); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("slack: Socket Mode read: %w", err)
		}
		var env envelope
		if json.Unmarshal(frame, &env) != nil {
			c.log.Debug("hub: slack: ignoring a frame that is not JSON")
			continue
		}
		switch env.Type {
		case "hello":
			c.connected.Store(true)
			c.log.Info("hub: slack: Socket Mode connected")
			continue
		case "disconnect":
			return errDisconnect{reason: safeCode(env.Reason)}
		}
		if env.EnvelopeID == "" {
			continue
		}
		if err := ack(env.EnvelopeID); err != nil {
			return fmt.Errorf("slack: Socket Mode ack: %w", err)
		}
		c.dispatch(ctx, env)
	}
}

// dispatch handles an acknowledged envelope on its own goroutine (bounded;
// a full pool answers nothing rather than blocking the connection).
func (c *Channel) dispatch(ctx context.Context, env envelope) {
	select {
	case c.sem <- struct{}{}:
	default:
		c.log.Warn("hub: slack: too many actions in flight; one was dropped", "type", env.Type)
		return
	}
	go func() {
		defer func() { <-c.sem }()
		defer recoverHubGoroutine("hub-slack-action")
		actCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		switch env.Type {
		case "interactive":
			c.handleInteractive(actCtx, env.Payload)
		case "slash_commands":
			c.handleSlashCommand(actCtx, env.Payload)
		}
	}()
}
