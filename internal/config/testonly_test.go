package config

// ValidateAutomations validates automations against profiles without knowing
// agent.command, so a rate_limited switch profile with an empty command
// (which inherits agent.command at dispatch) cannot have its switch_to_backend
// checked here. Callers that know agent.command (startup validation, the
// dashboard save handler, the cmd/itervox adapter) use
// ValidateAutomationsWithDefaults (CORE-010).
func ValidateAutomations(automations []AutomationConfig, profiles map[string]AgentProfile) error {
	return ValidateAutomationsWithDefaults(automations, profiles, "")
}
