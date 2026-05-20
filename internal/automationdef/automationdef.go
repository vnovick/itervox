package automationdef

// Trigger is the serializable automation trigger definition used by the API
// and WORKFLOW.md patching code.
type Trigger struct {
	Type     string `json:"type"`
	Cron     string `json:"cron,omitempty"`
	Timezone string `json:"timezone,omitempty"`
	State    string `json:"state,omitempty"`
}

// Filter is the serializable automation issue filter definition used by the
// API and WORKFLOW.md patching code.
type Filter struct {
	MatchMode         string   `json:"matchMode,omitempty"`
	States            []string `json:"states,omitempty"`
	LabelsAny         []string `json:"labelsAny,omitempty"`
	IdentifierRegex   string   `json:"identifierRegex,omitempty"`
	Limit             int      `json:"limit,omitempty"`
	InputContextRegex string   `json:"inputContextRegex,omitempty"`
	// MaxAgeMinutes skips stale input_required entries. Only meaningful on
	// input_required triggers; config validation rejects it on other types.
	MaxAgeMinutes int `json:"maxAgeMinutes,omitempty"`
}

// Policy is the serializable automation policy definition used by the API and
// WORKFLOW.md patching code.
type Policy struct {
	AutoResume      bool   `json:"autoResume,omitempty"`
	SwitchToProfile string `json:"switchToProfile,omitempty"`
	SwitchToBackend string `json:"switchToBackend,omitempty"`
	CooldownMinutes int    `json:"cooldownMinutes,omitempty"`
}

// Definition is the canonical serializable automation definition shared by
// HTTP settings payloads and WORKFLOW.md front-matter patching.
type Definition struct {
	ID           string  `json:"id"`
	Enabled      bool    `json:"enabled"`
	Profile      string  `json:"profile"`
	Instructions string  `json:"instructions,omitempty"`
	Trigger      Trigger `json:"trigger"`
	Filter       Filter  `json:"filter,omitempty"`
	Policy       Policy  `json:"policy,omitempty"`
}
