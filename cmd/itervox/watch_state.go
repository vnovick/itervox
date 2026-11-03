package main

import "time"

// The parts of GET /api/v1/state that `itervox watch` and `itervox approve`
// read (#240). Unknown fields are ignored, so an older or newer daemon
// decodes.

// watchState is the part of GET /api/v1/state that watch shows.
type watchState struct {
	MaxConcurrentAgents int            `json:"maxConcurrentAgents"`
	Capabilities        []string       `json:"capabilities"`
	Running             []watchRunning `json:"running"`
	Retrying            []struct {
		Identifier string    `json:"identifier"`
		Attempt    int       `json:"attempt"`
		DueAt      time.Time `json:"dueAt"`
		Error      string    `json:"error"`
	} `json:"retrying"`
	InputRequired   []watchInput `json:"inputRequired"`
	AutomationQueue []struct {
		Identifier   string `json:"identifier"`
		AutomationID string `json:"automationId"`
		Reason       string `json:"reason"`
	} `json:"automationQueue"`
	Paused           []string          `json:"paused"`
	PauseReasons     map[string]string `json:"pauseReasons"`
	DispatchPressure struct {
		EligibleWaiting     int `json:"eligibleWaiting"`
		BlockedByDependency int `json:"blockedByDependency"`
	} `json:"dispatchPressure"`
	// DispatchPaused is the daemon-wide dispatch pause (#213), nil when none.
	DispatchPaused *struct {
		Reason string `json:"reason"`
	} `json:"dispatchPaused"`
}

// watchRunning is one running issue: one lane.
type watchRunning struct {
	Identifier       string   `json:"identifier"`
	Title            string   `json:"title"`
	State            string   `json:"state"`
	Kind             string   `json:"kind"`
	Backend          string   `json:"backend"`
	Profile          string   `json:"profile"`
	WorkerHost       string   `json:"workerHost"`
	TurnCount        int      `json:"turnCount"`
	Tokens           int      `json:"tokens"`
	CostUSDEstimated *float64 `json:"costUsdEstimated"`
	ElapsedMs        int64    `json:"elapsedMs"`
	LastEvent        string   `json:"lastEvent"`
	LastEventAt      string   `json:"lastEventAt"`
}

// watchInput is one input-required entry.
type watchInput struct {
	Identifier        string `json:"identifier"`
	State             string `json:"state"`
	Context           string `json:"context"`
	GuardrailApproval bool   `json:"guardrailApproval"`
}

// waiting reports whether the entry waits for a human (not an answered
// question whose agent is resuming).
func (in watchInput) waiting() bool { return in.State == "" || in.State == "input_required" }
