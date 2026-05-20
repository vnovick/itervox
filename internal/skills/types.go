// Package skills implements the read-mostly skills inventory + runtime
// analytics surface described in
// `planning/plans/2026-04-16-skills-inventory-design.md`. It scans Claude /
// Codex skill, plugin, hook, MCP, and instruction-doc layouts under both the
// project working directory and the user home, and surfaces static issues plus
// runtime analytics. Capability graph fields are reserved for the next phase;
// v0.2.0 callers should treat Inventory as direct inventory/recommendation
// data.
//
// All scanning is read-only — no subprocess execution, no API calls, no
// destructive operations. One-click fixes from the analysis layer are opt-in
// with a confirm step in the dashboard, never automatic.
package skills

import "time"

// Inventory is the top-level snapshot returned by Scan. It is stable enough
// to be cached on disk between runs and recomputed on file mtime change.
type Inventory struct {
	ScanTime     time.Time
	Partial      bool                           // true when one or more scanners failed but best-effort data is available
	ScanError    string                         // joined scanner errors for partial inventories
	Stale        bool                           // true when tracked capability files changed or disappeared since ScanTime
	Profiles     map[string]ProfileCapabilities // reserved daemon-managed profile graph
	Capabilities []Capability                   // reserved normalized graph nodes
	Skills       []Skill                        // Claude + Codex + shared
	Plugins      []Plugin
	MCPServers   []MCPServer
	Hooks        []HookEntry
	Instructions []InstructionDoc
	Models       map[string][]ModelOption
	Schedules    []ScheduleConfig
	Runtime      *RuntimeEvidenceSnapshot
	Analytics    *AnalyticsSnapshot
	Issues       []InventoryIssue
}

// Skill describes a single SKILL.md (or equivalent) entry discovered under
// `.claude/skills/` (project / user / plugin) or `~/.codex/skills/`.
type Skill struct {
	Name            string
	Description     string
	Provider        string // "claude" | "codex" | "shared"
	Source          string // "project" | "user" | "system" | "plugin:{name}"
	FilePath        string
	ApproxTokens    int
	TriggerPatterns []string
	bodyText        string
}

// Capability is the reserved normalized graph node for a later phase. v0.2.0
// exposes direct inventory slices instead of populating a full graph.
type Capability struct {
	ID             string
	Kind           string // "skill" | "hook" | "plugin" | "instruction" | "mcp-server" | "tool-surface"
	Name           string
	Provider       string // "claude" | "codex" | "daemon" | "shared"
	Source         string
	FilePath       string
	ApproxTokens   int
	UsedByProfiles []string
	RuntimeSeen    bool
}

// InstructionDoc captures CLAUDE.md / AGENTS.md / system instruction layers.
type InstructionDoc struct {
	Name         string
	Provider     string // "claude" | "codex" | "shared"
	Scope        string // "project" | "user" | "system"
	FilePath     string
	ApproxTokens int
}

// Plugin is a Claude Code plugin (`.claude/plugins/<name>/plugin.json`) or a
// Codex equivalent. Aggregates child skills/hooks/agents/commands so the UI
// can present plugin-rooted views.
type Plugin struct {
	Name         string
	Provider     string
	FilePath     string
	Skills       []Skill
	Hooks        []HookEntry
	Agents       []AgentDef
	Commands     []CommandDef
	Source       string // "project" | "user" | "system"
	ApproxTokens int
}

// MCPServer describes an MCP server registered in settings.json or mcp.json.
type MCPServer struct {
	Name      string
	Transport string   // "stdio" | "sse" | "http"
	Command   string   // for stdio
	URL       string   // for sse/http
	Source    string   // "project-settings" | "user-settings" | "mcp.json"
	Tools     []string // discoverable from the manifest, when present
}

// HookEntry is a single hook registered in `.claude/settings.json::hooks` or
// equivalent. `Matcher` is the optional tool-pattern filter (e.g. "Bash") and
// is preserved verbatim from the source so the dashboard can render hook
// scope alongside the command.
type HookEntry struct {
	Event        string
	Matcher      string // optional — e.g. "Bash", "Read|Edit"
	Command      string
	Provider     string // "claude" | "codex" | "daemon"
	Source       string
	ApproxTokens int
}

// AnalyticsSnapshot is the Phase-2 runtime evidence projection.
type AnalyticsSnapshot struct {
	GeneratedAt        time.Time
	HasRuntimeEvidence bool
	SkillStats         []CapabilityStat
	HookStats          []CapabilityStat
	ProfileCosts       []ProfileCost
	Recommendations    []Recommendation
}

// CapabilityStat aggregates per-capability runtime usage data.
type CapabilityStat struct {
	CapabilityID    string
	Uses            int
	RuntimeLoads    int
	ApproxTokens    int
	LastSeenAt      *time.Time
	Configured      bool
	RuntimeVerified bool
}

// ProfileCost decomposes the estimated context cost of a single profile.
type ProfileCost struct {
	ProfileName            string
	TotalApproxTokens      int
	InstructionTokens      int
	SkillTokens            int
	HookTokens             int
	MCPToolSchemaTokens    int
	WorkflowTemplateTokens int
}

// Recommendation is the actionable analytics output (Phase 2).
type Recommendation struct {
	ID          string
	Severity    string
	Category    string // "cost" | "staleness" | "overlap" | "runtime-drift"
	Title       string
	Description string
	Affected    []string
}

// InventoryIssue is the actionable static-analysis output (Phase 1).
type InventoryIssue struct {
	ID          string   // e.g. "DUPLICATE_MCP"
	Severity    string   // "error" | "warn" | "info"
	Title       string   // human-readable
	Description string   // what's wrong and why it matters
	Affected    []string // node IDs (profile names, skill names, etc.)
	Fix         *Fix     // optional one-click resolution
}

// Fix is an opt-in one-click resolution for an InventoryIssue. The dashboard
// must surface `Destructive` as a confirm dialog.
type Fix struct {
	Label       string // "Remove duplicate" | "Consolidate" | etc.
	Action      string // "delete-file" | "edit-yaml" | "remove-mcp"
	Target      string // file path or config key
	Destructive bool   // if true, UI shows a confirm dialog
}

// RuntimeEvidenceSnapshot is the Phase-2 raw extraction from session logs.
type RuntimeEvidenceSnapshot struct {
	GeneratedAt        time.Time
	HasRuntimeEvidence bool
	SourceLogPaths     []string
	CapabilityLoads    map[string]int
	HookExecutionCount map[string]int
	ToolCallCount      map[string]int
}

// ProfileCapabilities is the daemon-managed view of which capabilities each
// profile carries. Populated from WORKFLOW.md profile definitions plus any
// auto-attached skills/hooks/MCP servers.
type ProfileCapabilities struct {
	ProfileName  string
	Skills       []string // capability IDs
	Hooks        []string
	MCPServers   []string
	Instructions []string
	ApproxTokens int
}

// AgentDef is a placeholder for the plugin-declared agent shape — the design
// draft refers to it but doesn't fully specify; T-79 fills in the fields.
type AgentDef struct {
	Name        string
	Description string
	FilePath    string
}

// CommandDef is a placeholder for the plugin-declared slash-command shape.
type CommandDef struct {
	Name        string
	Description string
	FilePath    string
}

// ScheduleConfig represents a cron-style scheduled job declared by a skill,
// plugin, or daemon. Phase-2 surface — placeholder for T-83.
type ScheduleConfig struct {
	Name     string
	Cron     string
	Provider string
	Source   string
}

// ModelOption mirrors the existing `web/src/types/schemas.ts ModelOption` for
// the daemon side. Kept here so the inventory can carry per-provider model
// catalogs without importing the server package.
type ModelOption struct {
	ID    string
	Label string
}
