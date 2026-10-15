package orchestrator

import (
	"context"
	"strings"

	"github.com/vnovick/itervox/internal/agent"
)

// Worker side of session steering (#178, see sessions.go and
// internal/agent/steer.go).

// runSteering returns the steering of the worker's run, or nil when the
// worker runs without a run handle (tests that call runWorker directly).
// Messages are written live only into a local Claude turn with
// agent.live_steering; otherwise they wait for the next resumed turn.
func (o *Orchestrator) runSteering(ctx context.Context, identifier, backend, workerHost string) *agent.Steering {
	h := runFrom(ctx)
	if h == nil || h.steer == nil {
		return nil
	}
	live := steeringFor(backend, workerHost, o.cfg.Agent.LiveSteering) == SteeringLive
	return agent.NewSteering(h.steer, live, func(m agent.SteerMessage) {
		o.reportSessionDelivery(ctx, identifier, []agent.SteerMessage{m}, SteeringLive)
	})
}

// reportSessionDelivery tells the event loop that msgs reached the session.
// It blocks (a dropped report would leave the messages "queued" and then
// "dropped" at exit) until the loop takes it or the run ends.
func (o *Orchestrator) reportSessionDelivery(ctx context.Context, identifier string, msgs []agent.SteerMessage, delivery string) {
	if len(msgs) == 0 {
		return
	}
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	select {
	case o.events <- OrchestratorEvent{
		Type:            EventSessionMessageDelivered,
		RunID:           runIDFrom(ctx),
		SessionDelivery: &SessionDelivery{Identifier: identifier, IDs: ids, Delivery: delivery},
	}:
	case <-ctx.Done():
	}
}

// steeringPrompt is the prompt of a resumed turn that delivers queued
// operator messages.
func steeringPrompt(msgs []agent.SteerMessage) string {
	var b strings.Builder
	if len(msgs) == 1 {
		b.WriteString("The operator sent you a message from the itervox Sessions panel while you were working.")
	} else {
		b.WriteString("The operator sent you messages from the itervox Sessions panel while you were working.")
	}
	b.WriteString(" Take it into account and continue the task.\n")
	for _, m := range msgs {
		b.WriteString("\nOperator message:\n")
		b.WriteString(m.Text)
		b.WriteString("\n")
	}
	return b.String()
}
