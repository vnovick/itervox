// Package statusui renders a live interactive status panel to stderr using
// the bubbletea TUI framework.
//
// Layout (horizontal split):
//
//	╭─ ITERVOX ────────────────────────────...
//	│ Agents: 2/10   Tokens: ↑12k ↓8k  Retrying: 0
//	│ Web: http://localhost:8090
//	┌──── ISSUES (k↑ j↓ tab expand) ─┐ │ ┌── LOGS: TIPRD-25 ↗ subagent ─┐
//	│ ▶ TIPRD-25 ▾ Running  t5 4.3k  │ │ │ ⚙ bash — grep -r "foo" ...   │
//	│     ↗ Explore collector         │ │ │ Claude analysed the code...   │
//	│   ▶ ↗ Analyse UI patterns       │ │ │ ⚙ str_replace_editor          │
//	│   TIPRD-26    Todo     t0    0  │ │ │                               │
//	│ ↻ RETRY QUEUE                   │ │ │                               │
//	╰────────────────────────────────────────...
//	↑/↓ select  tab expand  j/k scroll log  x pause  q quit
package statusui

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/server"
)

const (
	copyKeyHint      = "copy"
	copyPRHelpLabel  = "copy PR URL"
	copyWebHelpLabel = "copy web URL"
)

// Config holds optional display configuration passed from main.
type Config struct {
	DashboardURL string
	MaxAgents    int
	// TrackerKind is "linear" or "github", shown in the header.
	TrackerKind string
	// BacklogStates are the states considered "backlog" (not ready for work).
	// Issues in these states are shown in the BACKLOG section of the backlog panel.
	BacklogStates []string
	// TodoStates are the states considered "todo" (ready for work).
	// Issues in these states are shown in the TODO section of the backlog panel.
	TodoStates []string
	// FetchProjects loads the project list for the interactive picker.
	// If nil, the 'p' key is disabled. The function must not block indefinitely.
	FetchProjects func() ([]ProjectItem, error)
	// SetProjectFilter applies the selected project slugs.
	// An empty slice means "all issues"; nil resets to WORKFLOW.md default.
	SetProjectFilter func(slugs []string)
	// AdjustWorkers sends a delta (+1 or -1) to POST /api/v1/settings/workers.
	// If nil, the + / - keys are disabled.
	AdjustWorkers func(delta int)
	// FetchBacklog loads issues in backlog/todo states for the 'b' backlog panel.
	// If nil, the 'b' key is disabled.
	FetchBacklog func() ([]BacklogIssueItem, error)
	// DispatchIssue moves a backlog issue to the first active state to queue it
	// for agent processing. If nil, the 'enter' dispatch action is disabled.
	DispatchIssue func(identifier string) error
	// ResumeIssue resumes a paused agent. If nil, the 'r' key is disabled.
	ResumeIssue func(identifier string) bool
	// TerminateIssue hard-stops a running or paused agent without pausing it.
	// If nil, the 'd' key is disabled.
	TerminateIssue func(identifier string) bool
	// SetIssueProfile assigns (or clears) a per-issue agent profile override.
	// Empty profile string resets to default. If nil, the 'a' key is disabled.
	SetIssueProfile func(identifier, profile string)
	// IssueProfiles returns the current map of issue identifier to profile name.
	// If nil, profile badges are not shown.
	IssueProfiles func() map[string]string
	// TriggerPoll requests an immediate orchestrator poll cycle. Used to make
	// paused issues that were moved back to an active state in the tracker get
	// picked up faster than the normal poll interval (default 30 s).
	// If nil, no extra polling is triggered.
	TriggerPoll func()
	// FetchIssueDetail loads full issue details (title, description, labels,
	// priority, comments) for the split details pane. If nil, the issue
	// details section is not shown.
	FetchIssueDetail func(identifier string) (*BacklogIssueItem, error)
	// QuitApp shuts down the outer itervox process when the user presses q.
	// If nil, q only exits Bubble Tea.
	QuitApp func()
}

// ProjectItem is one entry in the TUI project picker.
type ProjectItem struct {
	ID   string
	Name string
	Slug string
}

// BacklogIssueItem is one issue in the backlog/todo panel.
type BacklogIssueItem struct {
	Identifier  string
	Title       string
	State       string
	Priority    int           // 0=none, 1=urgent, 2=high, 3=medium, 4=low
	Description string        // Issue description
	Comments    []CommentItem // Issue comments
}

// CommentItem is one comment on an issue.
type CommentItem struct {
	Author string
	Body   string
}

// tickMsg fires every second to refresh the snapshot.
type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// backlogLoadedMsg carries async-loaded backlog issues into the TUI update loop.
type backlogLoadedMsg struct {
	items []BacklogIssueItem
	err   error
}

// pickerLoadedMsg carries async-loaded project list into the TUI update loop.
type pickerLoadedMsg struct {
	projects []ProjectItem
	err      error
}

// ── Sci-fi colour palette ────────────────────────────────────────────────────
// Electric neon accents on a dark terminal background.
// All colours are 24-bit hex so they render correctly on modern terminals.
const (
	colCyan   = lipgloss.Color("#00d4ff") // primary accent — electric cyan
	colGreen  = lipgloss.Color("#00ff88") // active / success — neon green
	colAmber  = lipgloss.Color("#ffb000") // warning / tokens — amber
	colRed    = lipgloss.Color("#ff4040") // error / paused — hot red
	colPurple = lipgloss.Color("#bf5af2") // subagent — electric purple
	colGray   = lipgloss.Color("#4a5568") // dim chrome
	colMuted  = lipgloss.Color("#718096") // secondary text
	colSelect = lipgloss.Color("#00d4ff") // selected-row foreground
)

// Lipgloss styles.
var (
	styleBold    = lipgloss.NewStyle().Bold(true)
	styleDim     = lipgloss.NewStyle().Foreground(colMuted)
	styleGreen   = lipgloss.NewStyle().Foreground(colGreen).Bold(true)
	styleYellow  = lipgloss.NewStyle().Foreground(colAmber)
	styleCyan    = lipgloss.NewStyle().Foreground(colCyan)
	styleRed     = lipgloss.NewStyle().Foreground(colRed).Bold(true)
	styleGray    = lipgloss.NewStyle().Foreground(colGray)
	styleReverse = lipgloss.NewStyle().Foreground(colSelect).Bold(true)
	stylePurple  = lipgloss.NewStyle().Foreground(colPurple)
	styleLabel   = lipgloss.NewStyle().Foreground(colCyan).Bold(true) // section labels
	styleMuted   = lipgloss.NewStyle().Foreground(colMuted)
)

const (
	leftPaneWidth = 46 // visual chars for the issue list pane
	footerLines   = 2  // ╚═ line + help line
)

// toolStyle returns a lipgloss style for a given tool name based on its category.
func toolStyle(name string) lipgloss.Style {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "bash") || strings.Contains(n, "shell") || strings.Contains(n, "execute") || n == "sh":
		return styleYellow // amber — shell/command execution
	case strings.Contains(n, "read") || strings.Contains(n, "write") || strings.Contains(n, "edit") ||
		strings.Contains(n, "glob") || n == "ls" || strings.Contains(n, "file") || strings.Contains(n, "notebook"):
		return styleGreen // green — file operations
	case strings.Contains(n, "web") || strings.Contains(n, "fetch") || strings.Contains(n, "http") ||
		strings.Contains(n, "navigate") || strings.Contains(n, "browse") || strings.Contains(n, "url"):
		return styleCyan // cyan — web/network
	case strings.Contains(n, "task") || strings.Contains(n, "agent") || strings.Contains(n, "dispatch") ||
		strings.Contains(n, "subagent"):
		return stylePurple // purple — AI orchestration
	case strings.Contains(n, "grep") || strings.Contains(n, "search") || strings.Contains(n, "find"):
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#7dd3fc")) // sky — search
	default:
		return styleMuted
	}
}

// subagentInfo captures one subagent task boundary within a session's log slice.
// Lines [startLine, endLine) in the session's buffer belong to this subagent.
type subagentInfo struct {
	description string
	startLine   int
	endLine     int // exclusive; equals len(lines) for the last subagent
}

// leftItem is one navigable row in the left pane.
// subagentIdx == -1 means this is the issue header row (shows all logs).
// subagentIdx >= 0 means this is a subagent row (shows that subagent's log slice).
type leftItem struct {
	issueIdx    int
	subagentIdx int
	label       string // description for subagent rows
}

// Model is the root bubbletea model for the Itervox status UI.
type Model struct {
	snap        func() server.StateSnapshot
	buf         *logbuffer.Buffer
	cancelFn    func(string) bool
	cfg         Config
	sessions    []server.RunningRow
	history     []server.HistoryRow
	retrying    []server.RetryRow
	paused      []string
	inputRows   []server.InputRequiredRow
	navItems    []leftItem                // flat navigation list (issues + their subagents)
	selectedNav int                       // cursor into navItems
	subagents   map[string][]subagentInfo // per-identifier subagent segments
	collapsed   map[string]bool           // per-identifier; true = subagent rows hidden
	logVP       viewport.Model
	width       int
	height      int
	keys        keyMap
	help        help.Model
	ready       bool
	killMsg     string
	lastText    map[string]string // persists last text message per identifier

	// project picker state
	pickerOpen     bool
	pickerLoading  bool
	pickerErr      string
	pickerProjects []ProjectItem
	pickerCursor   int
	pickerSel      map[string]bool // slug → selected

	// backlog panel state
	backlogOpen    bool
	backlogLoading bool
	backlogErr     string
	backlogItems   []BacklogIssueItem
	backlogCursor  int
	dispatchMsg    string // feedback after dispatching a backlog issue

	// profile picker state
	profilePickerOpen   bool
	profilePickerCursor int
	profilePickerTarget string   // identifier of the issue being assigned
	profilePickerItems  []string // cached AvailableProfiles from snapshot

	// per-issue profile overrides, refreshed each tick from IssueProfiles callback
	profileOverrides map[string]string

	// split pane state
	splitMode         bool              // true = details pane visible on right
	splitVP           viewport.Model    // scrollable viewport for details pane
	splitReady        bool              // true after first splitVP initialization
	splitIssueDetail  *BacklogIssueItem // cached issue detail for split pane
	splitDetailTarget string            // identifier currently loaded in split detail

	// paused section navigation
	inPausedSection bool
	pausedCursor    int

	// history section navigation
	historyCursor int

	// Panel focus: 0=left, 1=right, 2=bottom(timeline)
	activePanel int
	// leftTab controls which sub-view is shown in the left panel: "" = issues, "history" = history
	leftTab string

	// Timeline drill-down state
	timelineCursor  int  // selected session bar in timeline
	timelineDetail  bool // true = showing subagent phase breakdown
	ganttEntryCount int  // number of entries in last rendered gantt (for cursor clamping)

	// lastPollTrigger is the time of the most recent TUI-initiated poll; used
	// to rate-limit TriggerPoll calls to at most once every 10 s.
	lastPollTrigger time.Time
}

// New constructs the root model.
// cancelFn is called when the user presses x to pause the selected running session.
func New(snap func() server.StateSnapshot, buf *logbuffer.Buffer, cfg Config, cancelFn func(string) bool) Model {
	if cfg.MaxAgents <= 0 {
		cfg.MaxAgents = 10
	}
	return Model{
		snap:             snap,
		buf:              buf,
		cancelFn:         cancelFn,
		cfg:              cfg,
		keys:             defaultKeys(),
		help:             help.New(),
		lastText:         make(map[string]string),
		subagents:        make(map[string][]subagentInfo),
		collapsed:        make(map[string]bool),
		pickerSel:        make(map[string]bool),
		profileOverrides: make(map[string]string),
	}
}

// Init implements tea.Model and starts the tick timer.
func (m Model) Init() tea.Cmd {
	return tickCmd()
}

// Update implements tea.Model and handles all TUI messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case backlogLoadedMsg:
		m.backlogLoading = false
		if msg.err != nil {
			m.backlogErr = "load failed: " + msg.err.Error()
			m.backlogOpen = false
			break
		}
		m.backlogItems = msg.items
		m.backlogOpen = true
		m.backlogCursor = 0
		m.backlogErr = ""

	case pickerLoadedMsg:
		m.pickerLoading = false
		if msg.err != nil {
			m.pickerErr = "load failed: " + msg.err.Error()
			m.pickerOpen = false
			break
		}
		m.pickerProjects = msg.projects
		m.pickerOpen = true
		m.pickerErr = ""
		// Initialise selection from current snapshot filter.
		snap := m.snap()
		m.pickerSel = make(map[string]bool)
		for _, slug := range snap.ActiveProjectFilter {
			m.pickerSel[slug] = true
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeViewport()
		if m.splitMode && m.width < 120 {
			m.splitMode = false
			m.resizeViewport()
		}

	case tickMsg:
		s := m.snap()
		m.sessions = s.Running
		m.history = s.History
		m.retrying = s.Retrying
		m.paused = s.Paused
		m.inputRows = s.InputRequired
		// Keep paused cursor in bounds after list changes.
		if m.inPausedSection {
			if len(m.paused) == 0 {
				m.inPausedSection = false
				m.pausedCursor = 0
			} else if m.pausedCursor >= len(m.paused) {
				m.pausedCursor = len(m.paused) - 1
			}
		}
		// Reset timeline detail if there's nothing to show.
		if len(m.sessions)+len(m.history) == 0 {
			m.timelineDetail = false
		}
		// Keep history cursor in bounds.
		histLen := len(m.history)
		if histLen == 0 {
			m.leftTab = ""
			m.historyCursor = 0
		} else if m.historyCursor >= histLen {
			m.historyCursor = histLen - 1
		}
		// Sync MaxAgents from snapshot so the header stays accurate after API changes.
		if s.MaxConcurrentAgents > 0 {
			m.cfg.MaxAgents = s.MaxConcurrentAgents
		}

		// Refresh per-issue profile overrides from the orchestrator.
		if m.cfg.IssueProfiles != nil {
			m.profileOverrides = m.cfg.IssueProfiles()
		}

		// Persist the most-recent text block per session.
		if m.buf != nil {
			for _, r := range m.sessions {
				lines := m.buf.Get(r.Identifier)
				for i := len(lines) - 1; i >= 0; i-- {
					if e, ok := parseBufLine(lines[i]); ok {
						if (e.Msg == "claude: text" || e.Msg == "codex: text") && e.Text != "" {
							m.lastText[r.Identifier] = e.Text
							break
						}
					}
				}
				// Rebuild subagent segments for this session.
				m.subagents[r.Identifier] = extractSubagents(lines)
			}
		}

		// Rebuild the flat navigation list, preserving the stable cursor position.
		stableID, stableSub := m.stableKey()
		m.navItems = buildNavItems(m.sessions, m.subagents, m.collapsed)
		m.selectedNav = findNavItem(m.navItems, m.sessions, stableID, stableSub)

		// Recompute gantt entry count so cursor bounds are correct in Update().
		// (View() uses a value receiver so mutations there don't persist.)
		m.ganttEntryCount = m.computeGanttEntryCount()
		if m.ganttEntryCount > 0 && m.timelineCursor >= m.ganttEntryCount {
			m.timelineCursor = m.ganttEntryCount - 1
		}

		// Trigger a background poll at half the configured poll interval so
		// that new or re-activated issues show up in the TUI in roughly half
		// the time they'd normally take, without exceeding the rate budget the
		// user chose via interval_ms (minimum 10 s to avoid hammering the API).
		if m.cfg.TriggerPoll != nil {
			triggerInterval := max(time.Duration(s.PollIntervalMs/2)*time.Millisecond, 10*time.Second)
			if time.Since(m.lastPollTrigger) > triggerInterval {
				m.cfg.TriggerPoll()
				m.lastPollTrigger = time.Now()
			}
		}

		if m.splitMode {
			m.refreshSplitDetail()
		}
		m.resizeViewport()
		m.refreshViewport()
		cmds = append(cmds, tickCmd())

	case tea.KeyMsg:
		// Backlog panel navigation takes priority when open.
		if m.backlogOpen {
			switch {
			case key.Matches(msg, m.keys.ListUp):
				if m.backlogCursor > 0 {
					m.backlogCursor--
				}
			case key.Matches(msg, m.keys.ListDown):
				if m.backlogCursor < len(m.backlogItems)-1 {
					m.backlogCursor++
				}
			case key.Matches(msg, m.keys.Dispatch):
				// Dispatch key (d/enter): dispatch the selected issue
				if m.cfg.DispatchIssue != nil && m.backlogCursor < len(m.backlogItems) {
					item := m.backlogItems[m.backlogCursor]
					if err := m.cfg.DispatchIssue(item.Identifier); err != nil {
						m.dispatchMsg = "✗ dispatch failed: " + truncate(err.Error(), 30)
					} else {
						m.dispatchMsg = "⚡ Queued " + item.Identifier
						m.backlogItems = append(m.backlogItems[:m.backlogCursor], m.backlogItems[m.backlogCursor+1:]...)
						if m.backlogCursor > 0 && m.backlogCursor >= len(m.backlogItems) {
							m.backlogCursor = len(m.backlogItems) - 1
						}
						// Trigger an immediate orchestrator poll so the newly-dispatched
						// issue appears without waiting for the next scheduled poll cycle.
						if m.cfg.TriggerPoll != nil {
							m.cfg.TriggerPoll()
							m.lastPollTrigger = time.Now()
						}
					}
				}
			case key.Matches(msg, m.keys.AssignProfile):
				if m.cfg.SetIssueProfile != nil && m.backlogCursor < len(m.backlogItems) {
					snap := m.snap()
					if len(snap.AvailableProfiles) > 0 {
						item := m.backlogItems[m.backlogCursor]
						m.profilePickerItems = snap.AvailableProfiles
						m.profilePickerTarget = item.Identifier
						m.profilePickerCursor = 0
						current := m.profileOverrides[item.Identifier]
						for i, p := range m.profilePickerItems {
							if p == current {
								m.profilePickerCursor = i
								break
							}
						}
						if current == "" {
							m.profilePickerCursor = len(m.profilePickerItems)
						}
						m.profilePickerOpen = true
					}
				}
			case key.Matches(msg, m.keys.EscKey), key.Matches(msg, m.keys.BacklogToggle), key.Matches(msg, m.keys.PickerClose):
				m.backlogOpen = false
				m.dispatchMsg = ""
			}
			break
		}

		// Profile picker navigation takes priority when open.
		if m.profilePickerOpen {
			switch {
			case key.Matches(msg, m.keys.ListUp):
				if m.profilePickerCursor > 0 {
					m.profilePickerCursor--
				}
			case key.Matches(msg, m.keys.ListDown):
				// +1 for "clear override" row
				total := len(m.profilePickerItems) + 1
				if m.profilePickerCursor < total-1 {
					m.profilePickerCursor++
				}
			case key.Matches(msg, m.keys.PickerApply), key.Matches(msg, m.keys.DrillDown):
				if m.cfg.SetIssueProfile != nil {
					var profile string
					if m.profilePickerCursor < len(m.profilePickerItems) {
						profile = m.profilePickerItems[m.profilePickerCursor]
					}
					// else: cursor is on "clear override" row, profile stays ""
					m.cfg.SetIssueProfile(m.profilePickerTarget, profile)
					if profile == "" {
						m.killMsg = "⚙ profile cleared for " + m.profilePickerTarget
					} else {
						m.killMsg = "⚙ " + profile + " assigned to " + m.profilePickerTarget
					}
					// Update local cache immediately so badge appears without waiting for tick.
					if profile == "" {
						delete(m.profileOverrides, m.profilePickerTarget)
					} else {
						m.profileOverrides[m.profilePickerTarget] = profile
					}
				}
				m.profilePickerOpen = false
			case key.Matches(msg, m.keys.PickerClose), key.Matches(msg, m.keys.EscKey):
				m.profilePickerOpen = false
			}
			break
		}

		// Picker navigation takes priority when the picker is open.
		if m.pickerOpen {
			switch {
			case key.Matches(msg, m.keys.ListUp):
				if m.pickerCursor > 0 {
					m.pickerCursor--
				}
			case key.Matches(msg, m.keys.ListDown):
				total := len(m.pickerProjects) + 2 // +2: "All" + "No project"
				if m.pickerCursor < total-1 {
					m.pickerCursor++
				}
			case key.Matches(msg, m.keys.PickerSel):
				slug := m.pickerSlugAt(m.pickerCursor)
				if m.pickerSel[slug] {
					delete(m.pickerSel, slug)
				} else {
					m.pickerSel[slug] = true
				}
			case key.Matches(msg, m.keys.PickerApply):
				m.applyPickerFilter()
				m.pickerOpen = false
			case key.Matches(msg, m.keys.PickerClose), key.Matches(msg, m.keys.Quit):
				m.pickerOpen = false
			}
			break
		}

		switch {
		case key.Matches(msg, m.keys.OpenPicker):
			if m.cfg.FetchProjects != nil && !m.pickerLoading {
				m.pickerLoading = true
				cmds = append(cmds, func() tea.Msg {
					projects, err := m.cfg.FetchProjects()
					return pickerLoadedMsg{projects: projects, err: err}
				})
			}
		case key.Matches(msg, m.keys.PanelNext):
			maxPanel := 3 // 0=left, 1=logs, 2=gantt
			if m.splitMode {
				maxPanel = 4 // 0=left, 1=logs, 2=details, 3=gantt
			}
			next := (m.activePanel + 1) % maxPanel
			// Skip gantt if not visible.
			ganttPanel := maxPanel - 1
			if next == ganttPanel && !m.ganttVisible() {
				next = 0
			}
			if next == ganttPanel && m.leftTab != "history" {
				m.timelineCursor = 0
			}
			m.activePanel = next
		case key.Matches(msg, m.keys.EscKey):
			if m.timelineDetail {
				m.timelineDetail = false
			} else if m.leftTab == "history" {
				m.leftTab = ""
			} else if m.inPausedSection {
				m.inPausedSection = false
			} else if m.activePanel != 0 {
				m.activePanel = 0
			}
		case key.Matches(msg, m.keys.ListUp):
			switch m.activePanel {
			case 1: // right pane (logs)
				m.logVP.ScrollUp(1)
			case 2:
				if m.splitMode {
					// details pane scroll
					m.splitVP.ScrollUp(1)
				} else {
					// timeline — arrows always move cursor; exits detail mode if needed
					m.ganttEntryCount = m.computeGanttEntryCount()
					if m.timelineCursor > 0 {
						m.timelineCursor--
						m.timelineDetail = false
					}
				}
			case 3: // timeline when split is active
				m.ganttEntryCount = m.computeGanttEntryCount()
				if m.timelineCursor > 0 {
					m.timelineCursor--
					m.timelineDetail = false
				}
			default: // left pane
				if m.leftTab == "history" {
					if m.historyCursor > 0 {
						m.historyCursor--
						m.refreshViewport()
						if m.splitMode {
							m.refreshSplitDetail()
						}
					}
				} else if m.inPausedSection {
					if m.pausedCursor > 0 {
						m.pausedCursor--
						m.refreshViewport()
						if m.splitMode {
							m.refreshSplitDetail()
						}
					} else {
						// Return to nav list, land on last item.
						m.inPausedSection = false
						if len(m.navItems) > 0 {
							m.selectedNav = len(m.navItems) - 1
						}
						m.refreshViewport()
						if m.splitMode {
							m.refreshSplitDetail()
						}
					}
					m.killMsg = ""
				} else if m.selectedNav > 0 {
					m.selectedNav--
					m.killMsg = ""
					m.timelineCursor = 0
					m.timelineDetail = false
					m.refreshViewport()
					if m.splitMode {
						m.refreshSplitDetail()
					}
				}
			}
		case key.Matches(msg, m.keys.ListDown):
			switch m.activePanel {
			case 1: // right pane (logs)
				m.logVP.ScrollDown(1)
			case 2:
				if m.splitMode {
					m.splitVP.ScrollDown(1)
				} else {
					m.ganttEntryCount = m.computeGanttEntryCount()
					if m.timelineCursor < m.ganttEntryCount-1 {
						m.timelineCursor++
					}
					m.timelineDetail = false
				}
			case 3: // timeline when split is active
				m.ganttEntryCount = m.computeGanttEntryCount()
				if m.timelineCursor < m.ganttEntryCount-1 {
					m.timelineCursor++
				}
				m.timelineDetail = false
			default: // left pane
				if m.leftTab == "history" {
					if m.historyCursor < len(m.history)-1 {
						m.historyCursor++
						m.refreshViewport()
						if m.splitMode {
							m.refreshSplitDetail()
						}
					}
				} else if m.inPausedSection {
					if m.pausedCursor < len(m.paused)-1 {
						m.pausedCursor++
						m.refreshViewport()
						if m.splitMode {
							m.refreshSplitDetail()
						}
					}
					m.killMsg = ""
				} else if m.selectedNav < len(m.navItems)-1 {
					m.selectedNav++
					m.killMsg = ""
					m.timelineCursor = 0
					m.timelineDetail = false
					m.refreshViewport()
					if m.splitMode {
						m.refreshSplitDetail()
					}
				} else if len(m.paused) > 0 {
					// At end of nav list — enter paused section.
					m.inPausedSection = true
					m.pausedCursor = 0
					m.killMsg = ""
					m.refreshViewport()
					if m.splitMode {
						m.refreshSplitDetail()
					}
				}
			}
		case key.Matches(msg, m.keys.Toggle):
			// Expand/collapse only when left panel is active (or not in a special mode).
			if m.activePanel == 0 {
				if item, ok := m.currentNavItem(); ok {
					if item.issueIdx < len(m.sessions) {
						id := m.sessions[item.issueIdx].Identifier
						m.collapsed[id] = !m.collapsed[id]
						// Jump to the issue header row when collapsing so cursor is not lost.
						if m.collapsed[id] {
							item.subagentIdx = -1
						}
						m.navItems = buildNavItems(m.sessions, m.subagents, m.collapsed)
						m.selectedNav = findNavItem(m.navItems, m.sessions, id, item.subagentIdx)
						m.refreshViewport()
					}
				}
			}
		case key.Matches(msg, m.keys.DrillDown):
			// Enter: drill down into timeline phases, or focus right pane for paused.
			if m.activePanel == 0 && m.inPausedSection {
				// Focus right pane to read paused session logs.
				m.activePanel = 1
			} else if m.activePanel == 2 && m.ganttVisible() && !m.timelineDetail {
				// Show phases for the cursor row (history mode) or selected session (normal mode).
				m.timelineDetail = true
			}
		case key.Matches(msg, m.keys.LogUp):
			pageSize := m.logVP.Height / 2
			if pageSize < 1 {
				pageSize = 3
			}
			m.logVP.ScrollUp(pageSize)
		case key.Matches(msg, m.keys.LogDown):
			pageSize := m.logVP.Height / 2
			if pageSize < 1 {
				pageSize = 3
			}
			m.logVP.ScrollDown(pageSize)
		case key.Matches(msg, m.keys.HistoryTab):
			if m.activePanel == 0 && !m.backlogOpen && !m.pickerOpen {
				if m.leftTab == "history" {
					m.leftTab = ""
				} else {
					m.leftTab = "history"
					m.historyCursor = 0
				}
				m.refreshViewport()
			}
		case key.Matches(msg, m.keys.OpenURL):
			if m.buf != nil {
				if id, _ := m.selectedSessionID(); id != "" {
					url := extractPRLink(m.buf.Get(id))
					if url != "" {
						copyURLToClipboardFn(url)
						m.killMsg = "📋 PR URL copied to clipboard — " + url
					}
				}
			}
		case key.Matches(msg, m.keys.OpenWebUI):
			if m.cfg.DashboardURL != "" {
				copyURLToClipboardFn(m.cfg.DashboardURL)
				m.killMsg = "📋 Web UI URL copied to clipboard — " + m.cfg.DashboardURL
			} else {
				m.killMsg = "⚠ web UI URL not available — set server.port in WORKFLOW.md"
			}
		case key.Matches(msg, m.keys.Kill):
			if m.inPausedSection {
				m.killMsg = "⏸ x pauses running issues — use r to resume or D to discard"
			} else if m.cancelFn != nil {
				if item, ok := m.currentNavItem(); ok && item.issueIdx < len(m.sessions) {
					id := m.sessions[item.issueIdx].Identifier
					if m.cancelFn(id) {
						m.killMsg = "⏸ Paused " + id + " — press r to resume"
					} else {
						m.killMsg = "✗ Could not pause " + id
					}
				} else {
					m.killMsg = "⏸ Select a running issue to pause"
				}
			} else {
				m.killMsg = "⏸ Pause is not configured"
			}
		case key.Matches(msg, m.keys.Resume):
			if m.cfg.ResumeIssue != nil && len(m.paused) > 0 {
				cursor := m.pausedCursor
				if cursor >= len(m.paused) {
					cursor = 0
				}
				id := m.paused[cursor]
				if m.cfg.ResumeIssue(id) {
					m.killMsg = "▶ Resumed " + id
					if cursor >= len(m.paused)-1 {
						m.inPausedSection = false
						m.pausedCursor = 0
					}
				} else {
					m.killMsg = "✗ Could not resume " + id
				}
			}
		case key.Matches(msg, m.keys.Terminate):
			// Discard selected item: running (via nav) or paused (via paused section).
			if m.cfg.TerminateIssue != nil {
				if m.inPausedSection && len(m.paused) > 0 {
					cursor := m.pausedCursor
					if cursor >= len(m.paused) {
						cursor = 0
					}
					id := m.paused[cursor]
					if m.cfg.TerminateIssue(id) {
						m.killMsg = "✕ Discarded " + id
						if cursor >= len(m.paused)-1 {
							m.inPausedSection = false
							m.pausedCursor = 0
						}
					} else {
						m.killMsg = "✗ Could not discard " + id
					}
				} else if item, ok := m.currentNavItem(); ok && item.issueIdx < len(m.sessions) {
					id := m.sessions[item.issueIdx].Identifier
					if m.cfg.TerminateIssue(id) {
						m.killMsg = "✕ Cancelled " + id
					} else {
						m.killMsg = "✗ Could not cancel " + id
					}
				}
			}
		case key.Matches(msg, m.keys.WorkersUp):
			if m.cfg.AdjustWorkers != nil {
				m.cfg.AdjustWorkers(1)
				if m.cfg.MaxAgents < 50 {
					m.cfg.MaxAgents++
				}
				m.killMsg = fmt.Sprintf("⚡ Workers → %d", m.cfg.MaxAgents)
			}
		case key.Matches(msg, m.keys.WorkersDown):
			if m.cfg.AdjustWorkers != nil {
				m.cfg.AdjustWorkers(-1)
				if m.cfg.MaxAgents > 1 {
					m.cfg.MaxAgents--
				}
				m.killMsg = fmt.Sprintf("⚡ Workers → %d", m.cfg.MaxAgents)
			}
		case key.Matches(msg, m.keys.BacklogToggle):
			if m.cfg.FetchBacklog != nil && !m.backlogLoading {
				if m.backlogOpen {
					m.backlogOpen = false
				} else {
					m.backlogLoading = true
					m.backlogErr = ""
					cmds = append(cmds, func() tea.Msg {
						items, err := m.cfg.FetchBacklog()
						return backlogLoadedMsg{items: items, err: err}
					})
				}
			}
		case key.Matches(msg, m.keys.AssignProfile):
			if m.cfg.SetIssueProfile != nil {
				snap := m.snap()
				if len(snap.AvailableProfiles) > 0 {
					var target string
					if m.backlogOpen && m.backlogCursor < len(m.backlogItems) {
						target = m.backlogItems[m.backlogCursor].Identifier
					} else if id, _ := m.selectedSessionID(); id != "" {
						target = id
					}
					if target != "" {
						m.profilePickerItems = snap.AvailableProfiles
						m.profilePickerTarget = target
						m.profilePickerCursor = 0
						// Pre-select current profile.
						current := m.profileOverrides[target]
						for i, p := range m.profilePickerItems {
							if p == current {
								m.profilePickerCursor = i
								break
							}
						}
						if current == "" {
							// Position on "clear override" row (last item).
							m.profilePickerCursor = len(m.profilePickerItems)
						}
						m.profilePickerOpen = true
					}
				}
			}
		case key.Matches(msg, m.keys.SplitToggle):
			if m.width >= 120 && !m.backlogOpen {
				m.splitMode = !m.splitMode
				m.resizeViewport()
				if m.splitMode {
					m.refreshSplitDetail()
				}
				m.refreshViewport()
			}
		case key.Matches(msg, m.keys.Quit):
			if m.cfg.QuitApp != nil {
				m.cfg.QuitApp()
			}
			return m, tea.Quit
		}
	}

	return m, tea.Batch(cmds...)
}

// currentNavItem returns the currently focused leftItem, if any.
func (m *Model) currentNavItem() (leftItem, bool) {
	if len(m.navItems) == 0 || m.selectedNav >= len(m.navItems) {
		return leftItem{}, false
	}
	return m.navItems[m.selectedNav], true
}

// stableKey returns the identifier and subagentIdx of the currently selected item
// so we can restore the cursor position after rebuilding navItems.
func (m *Model) stableKey() (id string, subIdx int) {
	if item, ok := m.currentNavItem(); ok && item.issueIdx < len(m.sessions) {
		return m.sessions[item.issueIdx].Identifier, item.subagentIdx
	}
	return "", -1
}

// resizeViewport updates the viewport dimensions when terminal size changes.
func (m *Model) resizeViewport() {
	if m.width == 0 || m.height == 0 {
		return
	}
	rightW := max(20, m.width-leftPaneWidth-1)
	bh := m.bodyHeight()
	vpH := max(3, bh-3) // 3 lines for right-pane title + info-row + separator

	if m.splitMode {
		logW := max(20, rightW*60/100)
		detailW := max(15, rightW-logW-1) // -1 for divider
		if !m.ready {
			m.logVP = viewport.New(logW, vpH)
			m.ready = true
		} else {
			m.logVP.Width = logW
			m.logVP.Height = vpH
		}
		if !m.splitReady {
			m.splitVP = viewport.New(detailW, vpH)
			m.splitReady = true
		} else {
			m.splitVP.Width = detailW
			m.splitVP.Height = vpH
		}
	} else {
		if !m.ready {
			m.logVP = viewport.New(rightW, vpH)
			m.ready = true
		} else {
			m.logVP.Width = rightW
			m.logVP.Height = vpH
		}
	}
}

// refreshSplitDetail loads issue details for the currently selected session
// into the split viewport. Skips if already loaded for the same identifier.
func (m *Model) refreshSplitDetail() {
	selID, _ := m.selectedSessionID()
	if selID == "" || selID == m.splitDetailTarget {
		return
	}
	m.splitDetailTarget = selID
	m.splitIssueDetail = nil
	if m.cfg.FetchIssueDetail != nil {
		detail, err := m.cfg.FetchIssueDetail(selID)
		if err == nil {
			m.splitIssueDetail = detail
		}
	}
}

// bodyHeight returns the number of lines available for the split panes.
func (m *Model) bodyHeight() int {
	gantt := 0
	if m.ganttVisible() {
		gantt = ganttSectionLines
	}
	return max(5, m.height-m.headerLineCount()-gantt-footerLines)
}

// ganttSectionLines is the fixed height of the Gantt timeline section.
// Title line + separator + up to maxGanttBars session bars + time axis.
const maxGanttBars = 4
const ganttSectionLines = maxGanttBars + 3

// ganttVisible reports whether the Gantt section should be rendered.
func (m *Model) ganttVisible() bool {
	// Hide timeline when backlog panel is open
	if m.backlogOpen || m.backlogLoading {
		return false
	}
	return len(m.sessions) > 0 || len(m.history) > 0
}

// computeGanttEntryCount returns the current number of renderable Gantt entries.
// Called on every key event in timeline panel so the count is never stale.
func (m *Model) computeGanttEntryCount() int {
	if m.leftTab != "history" {
		inSessions := make(map[string]bool, len(m.sessions))
		for _, r := range m.sessions {
			inSessions[r.Identifier] = true
		}
		n := len(m.sessions)
		for _, p := range m.paused {
			if !inSessions[p] {
				n++
			}
		}
		if n > maxGanttBars {
			n = maxGanttBars
		}
		return n
	}
	selID, _ := m.selectedSessionID()
	n := 0
	for _, r := range m.sessions {
		if r.Identifier == selID {
			n++
		}
	}
	for _, h := range m.history {
		if h.Identifier == selID {
			n++
		}
	}
	return n
}

// selectedSessionID returns the identifier and kind ("running"|"paused"|"history")
// for whichever item is currently highlighted in the left panel.
func (m *Model) selectedSessionID() (id, kind string) {
	// History sub-tab in left panel.
	if m.leftTab == "history" && m.historyCursor < len(m.history) {
		hi := len(m.history) - 1 - m.historyCursor
		return m.history[hi].Identifier, "history"
	}
	if m.inPausedSection && m.pausedCursor < len(m.paused) {
		return m.paused[m.pausedCursor], "paused"
	}
	if item, ok := m.currentNavItem(); ok && item.issueIdx < len(m.sessions) {
		return m.sessions[item.issueIdx].Identifier, "running"
	}
	return "", ""
}

// headerLineCount counts header lines rendered above the split panes.
func (m *Model) headerLineCount() int {
	n := 3 // ╔═, ║ Agents/Tokens/Retry, (optional rate/web/kill)
	s := m.snap()
	if s.RateLimits != nil {
		n++
	}
	if m.cfg.DashboardURL != "" {
		n++
	}
	if m.killMsg != "" || m.dispatchMsg != "" {
		n++
	}
	if len(s.InputRequired) > 0 {
		n++
	}
	return n
}

// refreshViewport rebuilds the log viewport for the selected nav item.
// Works for running, paused, and history sessions.
func (m *Model) refreshViewport() {
	if !m.ready || m.buf == nil {
		return
	}

	// Paused or history sub-tab: show logs for that identifier.
	if m.inPausedSection || m.leftTab == "history" {
		id, _ := m.selectedSessionID()
		if id == "" {
			m.logVP.SetContent(styleGray.Render("  No session selected."))
			return
		}
		lines := m.buf.Get(id)
		m.renderViewportLines(lines, id, "")
		return
	}

	item, ok := m.currentNavItem()
	if !ok || item.issueIdx >= len(m.sessions) {
		m.logVP.SetContent(styleGray.Render("  No session selected."))
		return
	}
	r := m.sessions[item.issueIdx]
	allLines := m.buf.Get(r.Identifier)

	var viewLines []string
	if item.subagentIdx == -1 {
		viewLines = allLines
	} else {
		subs := m.subagents[r.Identifier]
		if item.subagentIdx < len(subs) {
			sub := subs[item.subagentIdx]
			end := min(sub.endLine, len(allLines))
			viewLines = allLines[sub.startLine:end]
		}
	}
	m.renderViewportLines(viewLines, r.Identifier, "")
}

// renderViewportLines fills the log viewport with rendered+wrapped lines.
// identifier is used for the empty-state message. emptyMsg overrides the default.
func (m *Model) renderViewportLines(viewLines []string, identifier, emptyMsg string) {
	wrapW := max(m.logVP.Width, 10)

	var sb strings.Builder
	for _, line := range viewLines {
		rendered := colorLine(line)
		if rendered == "" {
			continue
		}
		wrapped := xansi.Wordwrap(rendered, wrapW, " ")
		sb.WriteString(wrapped)
		sb.WriteString("\n")
	}
	if sb.Len() == 0 {
		if emptyMsg != "" {
			sb.WriteString(styleGray.Render("  " + emptyMsg))
		} else if m.inPausedSection || m.leftTab == "history" {
			sb.WriteString(styleGray.Render("  No logs available for " + identifier + "."))
		} else {
			sb.WriteString(styleGray.Render("  Waiting for log output..."))
		}
	}
	wasAtBottom := m.logVP.AtBottom() || m.logVP.TotalLineCount() == 0
	m.logVP.SetContent(sb.String())
	if wasAtBottom {
		m.logVP.GotoBottom()
	}
}

// View implements tea.Model and renders the full TUI layout.
func (m Model) View() string {
	if !m.ready {
		return "Initializing Itervox...\n"
	}
	s := m.snap()

	// ── Header (full width) ─────────────────────────────────
	var totalIn, totalOut int
	for _, r := range s.Running {
		totalIn += r.InputTokens
		totalOut += r.OutputTokens
	}

	// ── Angular sci-fi header ────────────────────────────────
	// Top bar: ╔═[ ITERVOX ]═══...═╗
	innerW := max(0, m.width-2)
	title := "[ ITER//VOX ]"
	ruleLen := max(0, innerW-len(title)-1)
	hdrTop := styleGray.Render("╔═") +
		styleCyan.Bold(true).Render(title) +
		styleGray.Render(strings.Repeat("═", ruleLen)+"╗")

	// Backend display: collect unique backends from running sessions
	backendSet := make(map[string]bool)
	for _, r := range s.Running {
		if r.Backend != "" {
			backendSet[r.Backend] = true
		}
	}
	backendPart := ""
	if len(backendSet) > 0 {
		backends := make([]string, 0, len(backendSet))
		for b := range backendSet {
			backends = append(backends, b)
		}
		sort.Strings(backends)
		backendPart = styleGray.Render("   ") +
			styleLabel.Render("BACKEND") + styleGray.Render(" ▸ ") +
			styleCyan.Render(strings.Join(backends, ", "))
	}

	// Agent / token / retry row
	agentVal := styleGreen.Render(fmt.Sprintf("%d", s.Counts.Running)) +
		styleGray.Render(fmt.Sprintf("/%d", m.cfg.MaxAgents))
	tokenVal := styleYellow.Render("↑"+fmtCount(totalIn)) +
		styleMuted.Render(" ↓"+fmtCount(totalOut)) +
		styleGray.Render(" ∑"+fmtCount(totalIn+totalOut))
	retryColor := styleMuted
	if len(s.Retrying) > 0 {
		retryColor = styleRed
	}
	retryVal := retryColor.Render(fmt.Sprintf("%d", len(s.Retrying)))

	row1 := styleGray.Render("║ ") +
		styleLabel.Render("AGENTS") + styleGray.Render(" ▸ ") + agentVal +
		styleGray.Render("   ") +
		styleLabel.Render("TOKENS") + styleGray.Render(" ▸ ") + tokenVal +
		styleGray.Render("   ") +
		styleLabel.Render("RETRY") + styleGray.Render(" ▸ ") + retryVal +
		backendPart

	var hdr strings.Builder
	hdr.WriteString(hdrTop + "\n")
	hdr.WriteString(row1 + "\n")

	if waiting, pending := inputStateCounts(s.InputRequired); waiting > 0 || pending > 0 {
		var parts []string
		if waiting > 0 {
			parts = append(parts, styleYellow.Render(fmt.Sprintf("%d waiting", waiting)))
		}
		if pending > 0 {
			parts = append(parts, styleCyan.Render(fmt.Sprintf("%d resuming", pending)))
		}
		hdr.WriteString(styleGray.Render("║ ") +
			styleLabel.Render("INPUT ") + styleGray.Render(" ▸ ") +
			strings.Join(parts, styleGray.Render("   ")) +
			styleMuted.Render("  reply in tracker/dashboard") + "\n")
	}

	if m.cfg.DashboardURL != "" {
		hdr.WriteString(styleGray.Render("║ ") +
			styleLabel.Render("WEB   ") + styleGray.Render(" ▸ ") +
			styleCyan.Render(osc8Link(m.cfg.DashboardURL, m.cfg.DashboardURL)) +
			styleMuted.Render("  w:"+copyKeyHint) + "\n")
	}

	// GitHub tracker info: states are mapped to issue labels
	if m.cfg.TrackerKind == "github" {
		hdr.WriteString(styleGray.Render("║ ") +
			styleMuted.Render("ⓘ GitHub: issue states mapped to labels") + "\n")
	}

	statusMsg := m.killMsg
	if m.dispatchMsg != "" {
		statusMsg = m.dispatchMsg
	}
	if statusMsg != "" {
		hdr.WriteString(styleGray.Render("║ ") + styleYellow.Render("⚡ "+statusMsg) + "\n")
	}

	// Config-invalid banner (T-26 piece 4) — surface a stale-config state to
	// the operator so they know their last WORKFLOW.md edit didn't take and
	// the daemon is running on the previously-valid config. Mirrors the web
	// dashboard banner behavior.
	if s.ConfigInvalid != nil {
		ci := s.ConfigInvalid
		hdr.WriteString(styleGray.Render("║ ") +
			styleRed.Bold(true).Render("⚠ CONFIG INVALID") +
			styleGray.Render(" ▸ ") +
			styleYellow.Render(ci.Error) +
			styleMuted.Render(fmt.Sprintf("  (retry %d, daemon on last valid config)", ci.RetryAttempt)) +
			"\n")
	}

	// ── Split body ──────────────────────────────────────────
	left := m.renderLeft()
	divider := m.renderDivider()
	right := m.renderRight()
	var body string
	if m.splitMode && !m.backlogOpen {
		splitDiv := m.renderSplitDivider()
		details := m.renderSplitDetails()
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, divider, right, splitDiv, details)
	} else {
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, divider, right)
	}

	// ── Gantt timeline (shown when sessions are active) ─────
	gantt := ""
	if m.ganttVisible() {
		gantt = m.renderGantt() + "\n"
	}

	// ── Footer ──────────────────────────────────────────────
	footerRule := strings.Repeat("═", max(0, m.width-2))
	footer := styleGray.Render("╚"+footerRule+"╝") + "\n" + styleMuted.Render(m.help.View(m.keys))

	return hdr.String() + body + "\n" + gantt + footer + "\n"
}

// renderLeft builds the left pane (issue list with subagents, retry queue, paused, input).
// It always produces exactly bodyHeight lines, each leftPaneWidth chars wide.
func (m *Model) renderLeft() string {
	if m.profilePickerOpen {
		return m.renderProfilePicker()
	}
	if m.backlogOpen || m.backlogLoading {
		return m.renderBacklogPanel()
	}
	if m.pickerOpen || m.pickerLoading {
		return m.renderProjectPicker()
	}

	bh := m.bodyHeight()
	leftStyle := lipgloss.NewStyle().Width(leftPaneWidth).MaxWidth(leftPaneWidth)

	var lines []string
	add := func(content string) {
		if len(lines) < bh {
			lines = append(lines, leftStyle.Render(content))
		}
	}

	// Show active project filter if set.
	snap := m.snap()
	filterHint := ""
	if snap.TrackerKind == "linear" {
		switch {
		case snap.ActiveProjectFilter == nil:
			filterHint = styleGray.Render(" (default project)")
		case len(snap.ActiveProjectFilter) == 0:
			filterHint = styleCyan.Render(" (all projects)")
		default:
			filterHint = styleCyan.Render(fmt.Sprintf(" (%d project(s))", len(snap.ActiveProjectFilter)))
		}
	}
	leftHdrStyle := styleLabel
	issuesTab := "ISSUES"
	historyTabLabel := "history"
	if m.leftTab == "history" {
		issuesTab = "issues"
		historyTabLabel = "HISTORY"
	}
	leftHeader := fmt.Sprintf("◆─[ %s · %s ]", issuesTab, historyTabLabel)
	if m.activePanel == 0 {
		leftHeader = fmt.Sprintf("●─[ %s · %s ]", issuesTab, historyTabLabel)
		leftHdrStyle = styleGreen.Bold(true)
	}
	hHint := styleMuted.Render("  h:toggle")
	add(leftHdrStyle.Render(leftHeader) + filterHint + hHint)
	add(styleGray.Render(strings.Repeat("━", leftPaneWidth)))
	add(styleMuted.Render("  x pause  r resume  D discard"))

	// ── History sub-tab ──────────────────────────────────────────────────
	if m.leftTab == "history" {
		if len(m.history) == 0 {
			add(styleMuted.Render("  ○ no completed runs yet"))
		} else {
			// Deduplicate: keep only the latest run per identifier.
			seen := make(map[string]bool)
			var dedupHist []server.HistoryRow
			for i := len(m.history) - 1; i >= 0; i-- {
				h := m.history[i]
				if !seen[h.Identifier] {
					seen[h.Identifier] = true
					dedupHist = append(dedupHist, h)
				}
			}
			maxHist := min(bh-2, len(dedupHist))
			for i := 0; i < maxHist && len(lines) < bh; i++ {
				h := dedupHist[i]
				isCursor := m.historyCursor == i
				var statusGlyph string
				var statusStyle lipgloss.Style
				switch h.Status {
				case "succeeded":
					statusGlyph = "✓"
					statusStyle = styleGreen
				case "failed":
					statusGlyph = "✗"
					statusStyle = styleRed
				default:
					statusGlyph = "⊘"
					statusStyle = styleMuted
				}
				elapsed := time.Duration(h.ElapsedMs) * time.Millisecond
				tok := fmtCount(h.TotalTokens)
				idStr := truncate(h.Identifier, 13)
				row := fmt.Sprintf("  %s %-13s t%-2d %5s %s",
					statusGlyph, idStr, h.TurnCount, tok, fmtDuration(elapsed))
				if isCursor {
					padded := fmt.Sprintf("%-*s", leftPaneWidth-2, "► "+row[2:])
					if len(padded) > leftPaneWidth-2 {
						padded = padded[:leftPaneWidth-2]
					}
					add(statusStyle.Bold(true).Render(padded))
				} else {
					add(statusStyle.Faint(true).Render(row))
				}
			}
		}
		for len(lines) < bh {
			lines = append(lines, leftStyle.Render(""))
		}
		return strings.Join(lines, "\n")
	}

	if len(m.sessions) == 0 && len(m.retrying) == 0 {
		add(styleMuted.Render("  ○ no active sessions"))
		add(styleMuted.Render("  · awaiting issues..."))
	}

	for _, r := range m.sessions {
		if len(lines) >= bh {
			break
		}

		// Find the nav item index for this issue's header row.
		navIdx := findNavItem(m.navItems, m.sessions, r.Identifier, -1)
		selected := navIdx == m.selectedNav
		subs := m.subagents[r.Identifier]
		isCollapsed := m.collapsed[r.Identifier]

		// Build the expand/collapse indicator.
		expandMark := ""
		if len(subs) > 0 {
			if isCollapsed {
				expandMark = styleGray.Render(" ▸")
			} else {
				expandMark = styleGray.Render(" ▾")
			}
		}

		// State badge — coloured bracket tag
		stateStr := strings.ToUpper(truncate(r.State, 9))
		var stateBadge string
		sl := strings.ToLower(r.State)
		switch {
		case strings.Contains(sl, "running"), strings.Contains(sl, "progress"):
			stateBadge = styleGreen.Render("[◉ " + stateStr + "]")
		case strings.Contains(sl, "review"), strings.Contains(sl, "done"):
			stateBadge = styleCyan.Render("[✓ " + stateStr + "]")
		default:
			stateBadge = styleMuted.Render("[· " + stateStr + "]")
		}
		turns := fmt.Sprintf("t%-2d", r.TurnCount)
		tok := fmtCount(r.InputTokens + r.OutputTokens)

		profileBadge := ""
		if prof := m.profileOverrides[r.Identifier]; prof != "" {
			profileBadge = " " + styleYellow.Render("⚙"+truncate(prof, 8))
		}

		if selected {
			id := styleReverse.Render("▶ " + truncate(r.Identifier, 13))
			row := fmt.Sprintf("%s %s%s %s %4s %s", id, expandMark, profileBadge, turns, tok, stateBadge)
			padded := fmt.Sprintf("%-*s", leftPaneWidth, row)
			if len(padded) > leftPaneWidth {
				padded = padded[:leftPaneWidth]
			}
			add(padded)
		} else {
			id := styleMuted.Render("  " + truncate(r.Identifier, 13))
			row := fmt.Sprintf("%s %s%s %s %4s %s", id, expandMark, profileBadge, turns, tok, stateBadge)
			add(row)
		}

		// Text preview line: shown when no subagents (and not collapsed), or when collapsed.
		// Falls back to elapsed time when no text is available yet, so the row always
		// shows a live indicator that the session is running.
		showPreview := (len(subs) == 0 && !isCollapsed) || isCollapsed
		if showPreview {
			txt := m.lastText[r.Identifier]
			if txt == "" {
				txt = "⏱ " + fmtDuration(time.Duration(r.ElapsedMs)*time.Millisecond)
			}
			preview := "  " + truncate(txt, leftPaneWidth-3)
			if selected {
				add(styleDim.Render(fmt.Sprintf("%-*s", leftPaneWidth, preview)))
			} else {
				add(styleGray.Render(preview))
			}
		}

		// Subagent rows (only when not collapsed).
		if !isCollapsed && len(subs) > 0 {
			for subIdx, sub := range subs {
				if len(lines) >= bh {
					break
				}
				// Find nav idx for this subagent row.
				subNavIdx := findNavItem(m.navItems, m.sessions, r.Identifier, subIdx)
				subSelected := subNavIdx == m.selectedNav

				desc := truncate(sub.description, leftPaneWidth-8)
				var subRow string
				if subSelected {
					subRow = fmt.Sprintf("    ▶ ◈ %s", desc)
					padded := fmt.Sprintf("%-*s", leftPaneWidth, subRow)
					if len(padded) > leftPaneWidth {
						padded = padded[:leftPaneWidth]
					}
					add(stylePurple.Bold(true).Render(padded))
				} else {
					subRow = fmt.Sprintf("      ◈ %s", desc)
					add(stylePurple.Faint(true).Render(subRow))
				}
			}
		}
	}

	// Retry queue section.
	if len(m.retrying) > 0 && len(lines) < bh {
		add(styleGray.Render(strings.Repeat("━", leftPaneWidth)))
		add(styleLabel.Render("◆─[ RETRY QUEUE ]"))
		for _, r := range m.retrying {
			if len(lines) >= bh {
				break
			}
			due := max(0, time.Until(r.DueAt))
			errSuffix := ""
			if r.Error != "" {
				errSuffix = " " + truncate(r.Error, 12)
			}
			row := fmt.Sprintf("  ↻ %-12s att=%-2d in %s%s",
				truncate(r.Identifier, 12), r.Attempt, fmtDuration(due), errSuffix)
			add(styleYellow.Faint(true).Render(row))
		}
	}

	// Paused section.
	if len(m.paused) > 0 && len(lines) < bh {
		add(styleGray.Render(strings.Repeat("━", leftPaneWidth)))
		add(styleRed.Render("◆─[ PAUSED ]") + styleMuted.Render("  j↓ k↑ r resume  D discard"))
		for i, id := range m.paused {
			if len(lines) >= bh {
				break
			}
			isCursorHere := m.inPausedSection && m.pausedCursor == i
			if isCursorHere {
				row := fmt.Sprintf("► ⏸ %-12s  press r to resume", truncate(id, 12))
				padded := fmt.Sprintf("%-*s", leftPaneWidth, row)
				if len(padded) > leftPaneWidth {
					padded = padded[:leftPaneWidth]
				}
				add(styleRed.Bold(true).Render(padded))
			} else {
				row := fmt.Sprintf("  ⏸ %-12s  press r to resume", truncate(id, 12))
				add(styleRed.Faint(true).Render(row))
			}
		}
	}

	if len(m.inputRows) > 0 && len(lines) < bh {
		add(styleGray.Render(strings.Repeat("━", leftPaneWidth)))
		add(styleYellow.Render("◆─[ INPUT ]") + styleMuted.Render("  reply in tracker/dashboard"))
		for _, row := range m.inputRows {
			if len(lines) >= bh {
				break
			}
			glyph := "?"
			status := "waiting for reply"
			rowStyle := styleYellow.Faint(true)
			if row.State == "pending_input_resume" {
				glyph = "↺"
				status = "reply received"
				rowStyle = styleCyan.Faint(true)
			}
			rendered := fmt.Sprintf("  %s %-12s %s", glyph, truncate(row.Identifier, 12), status)
			add(rowStyle.Render(rendered))
		}
	}

	for len(lines) < bh {
		lines = append(lines, leftStyle.Render(""))
	}

	return strings.Join(lines, "\n")
}

// renderDivider renders a single-char-wide vertical separator of bodyHeight lines.
func (m *Model) renderDivider() string {
	bh := m.bodyHeight()
	lines := make([]string, bh)
	for i := range lines {
		lines[i] = styleGray.Render("║")
	}
	return strings.Join(lines, "\n")
}

// renderRight builds the right pane: title + separator + log viewport or tools view.
// Works for running, paused, and history sessions.
// When backlog panel is open, automatically shows selected issue details.
func (m *Model) renderRight() string {
	rightW := m.logVP.Width
	rightStyle := lipgloss.NewStyle().Width(rightW).MaxWidth(rightW)
	sep := rightStyle.Render(styleGray.Render(strings.Repeat("━", rightW)))

	// If backlog panel is open, always show issue details (auto-select).
	if m.backlogOpen && len(m.backlogItems) > 0 && m.backlogCursor >= 0 && m.backlogCursor < len(m.backlogItems) {
		return m.renderBacklogDetails()
	}

	// Collect metadata for the selected session regardless of its kind.
	selID, selKind := m.selectedSessionID()

	var selState, subLabel string
	var selTurns, selTok int

	switch selKind {
	case "running":
		if item, ok := m.currentNavItem(); ok && item.issueIdx < len(m.sessions) {
			r := m.sessions[item.issueIdx]
			selState = r.State
			selTurns = r.TurnCount
			selTok = r.InputTokens + r.OutputTokens
			if item.subagentIdx >= 0 {
				subs := m.subagents[r.Identifier]
				if item.subagentIdx < len(subs) {
					subLabel = " ↗ " + truncate(subs[item.subagentIdx].description, 40)
				}
			}
		}
	case "paused":
		selState = "paused"
	case "history":
		if m.historyCursor < len(m.history) {
			hi := len(m.history) - 1 - m.historyCursor
			h := m.history[hi]
			selState = h.Status
			selTurns = h.TurnCount
			selTok = h.TotalTokens
		}
	}

	// Panel focus styling.
	rightHdrStyle := styleLabel
	rightHeader := "◆─"
	if m.activePanel == 1 {
		rightHeader = "●─"
		rightHdrStyle = styleGreen.Bold(true)
	}

	splitHint := ""
	if m.width >= 120 {
		splitHint = styleMuted.Render("  s:details")
	}

	// Kind badge appended to title.
	var kindBadge string
	switch selKind {
	case "paused":
		kindBadge = styleRed.Render(" ⏸")
	case "history":
		switch selState {
		case "succeeded":
			kindBadge = styleGreen.Render(" ✓")
		case "failed":
			kindBadge = styleRed.Render(" ✗")
		default:
			kindBadge = styleMuted.Render(" ⊘")
		}
	}

	var titleText string
	if selID == "" {
		titleText = rightHdrStyle.Render(rightHeader+"[ LOGS ]") + splitHint
	} else {
		panelHint := ""
		if m.activePanel == 1 {
			panelHint = styleMuted.Render("  ↑/↓ scroll")
		}
		meta := ""
		if selTurns > 0 || selTok > 0 {
			meta = styleMuted.Render(fmt.Sprintf("  t%d  %s", selTurns, fmtCount(selTok)))
		}
		titleText = rightHdrStyle.Render(rightHeader+"[ LOGS: "+selID+subLabel+" ]") +
			kindBadge + meta + splitHint + panelHint
	}
	title := rightStyle.Render(titleText)

	// Show PR link and state info row between header and log body.
	var infoRow string
	if selID != "" && m.buf != nil {
		prURL := extractPRLink(m.buf.Get(selID))
		var parts []string
		if selState != "" {
			stateSl := strings.ToLower(selState)
			var stateRender string
			switch {
			case strings.Contains(stateSl, "progress"), strings.Contains(stateSl, "running"):
				stateRender = styleGreen.Render("[◉ " + strings.ToUpper(selState) + "]")
			case strings.Contains(stateSl, "review"), strings.Contains(stateSl, "done"), selState == "succeeded":
				stateRender = styleCyan.Render("[✓ " + strings.ToUpper(selState) + "]")
			case selState == "failed":
				stateRender = styleRed.Render("[✗ FAILED]")
			default:
				stateRender = styleMuted.Render("[· " + strings.ToUpper(selState) + "]")
			}
			parts = append(parts, stateRender)
		}
		if prURL != "" {
			parts = append(parts, styleCyan.Render("PR ↗ "+osc8Link(prURL, prURL))+styleMuted.Render("  o:"+copyKeyHint))
		}
		if len(parts) > 0 {
			infoRow = rightStyle.Render(styleGray.Render("  ")+strings.Join(parts, styleMuted.Render("  "))) + "\n"
		}
	}
	// Always emit exactly 1 infoRow line to keep the height budget consistent.
	// The viewport is sized bh-3 (title + infoRow + sep), so this line must always exist.
	if infoRow == "" {
		infoRow = "\n"
	}
	return title + "\n" + infoRow + sep + "\n" + m.logVP.View()
}

func inputStateCounts(rows []server.InputRequiredRow) (waiting, pending int) {
	for _, row := range rows {
		if row.State == "pending_input_resume" {
			pending++
			continue
		}
		waiting++
	}
	return waiting, pending
}

// prefix renders a coloured terminal-style prefix symbol followed by the message.
func termLine(pfx, pfxColor, msg, msgColor string) string {
	p := lipgloss.NewStyle().Foreground(lipgloss.Color(pfxColor)).Bold(true).Render(pfx)
	m := lipgloss.NewStyle().Foreground(lipgloss.Color(msgColor)).Render(msg)
	return p + " " + m
}

// parseBufLine unmarshals a JSON log buffer line into a BufLogEntry.
// Returns (entry, true) on success; (zero, false) if the line is not valid JSON.
func parseBufLine(line string) (domain.BufLogEntry, bool) {
	var e domain.BufLogEntry
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return domain.BufLogEntry{}, false
	}
	return e, true
}

// colorLine applies terminal-style colour + prefix to a log buffer line.
// Lines are JSON-encoded BufLogEntry values (written by formatBufLine).
// Returns "" for noise/lifecycle events that should not appear in the log pane.
func colorLine(line string) string {
	e, ok := parseBufLine(line)
	if !ok {
		return "" // skip unparseable lines
	}

	switch e.Msg {
	case "claude: text", "codex: text":
		if e.Text != "" {
			return termLine(">", "#00ff88", e.Text, "#e2e8f0")
		}
		return ""

	case "claude: subagent", "codex: subagent":
		msg := e.Description
		if msg == "" {
			msg = e.Tool + " (subagent)"
		}
		return termLine("◈", "#bf5af2", msg, "#d8b4fe")

	case "codex: action_detail":
		return "" // structured shell metadata — suppressed from display pane

	case "claude: action_started", "codex: action_started":
		msg := e.Tool + "…"
		if e.Description != "" {
			msg = e.Tool + "  " + e.Description + "…"
		}
		return termLine("⧖", "#6b7280", msg, "#9ca3af")

	case "claude: action", "codex: action":
		msg := e.Tool
		if e.Description != "" {
			msg = e.Tool + "  " + e.Description
		}
		if msg == "" {
			msg = e.Msg
		}
		return termLine("$", "#ffb000", msg, "#cbd5e1")

	case "claude: todo", "codex: todo":
		task := e.Task
		if task == "" {
			task = e.Msg
		}
		return termLine("☐", "#ffb000", task, "#cbd5e1")

	case "claude: session started", "claude: turn done",
		"codex: session started", "codex: turn done":
		return "" // skip internal lifecycle noise

	case "claude: result error", "codex: result error":
		return "" // skip internal lifecycle noise
	}

	switch {
	case strings.HasPrefix(e.Msg, "worker:"):
		return termLine("~", "#00d4ff", e.Msg, "#7dd3fc")
	case e.Level == "WARN":
		return termLine("⚡", "#ffb000", e.Msg, "#fcd34d")
	case e.Level == "ERROR":
		return termLine("✗", "#ff4040", e.Msg, "#fca5a5")
	default:
		if e.Msg == "" {
			return ""
		}
		return termLine("·", "#4a5568", e.Msg, "#718096")
	}
}

// extractSubagents scans log lines and returns one subagentInfo per
// "INFO claude: subagent" or "INFO codex: subagent" boundary found. Lines between consecutive
// subagent markers (or to end-of-slice) belong to that subagent.
func extractSubagents(lines []string) []subagentInfo {
	subs := make([]subagentInfo, 0, len(lines))
	for i, line := range lines {
		e, ok := parseBufLine(line)
		if !ok {
			continue
		}
		if e.Msg != "claude: subagent" && e.Msg != "codex: subagent" {
			continue
		}
		desc := e.Description
		if desc == "" {
			desc = e.Tool
		}
		if len(subs) > 0 {
			subs[len(subs)-1].endLine = i
		}
		subs = append(subs, subagentInfo{
			description: desc,
			startLine:   i,
			endLine:     len(lines),
		})
	}
	return subs
}

// buildNavItems produces the flat navigation list from current sessions, their
// subagents, and which sessions are collapsed.
func buildNavItems(sessions []server.RunningRow, subagents map[string][]subagentInfo, collapsed map[string]bool) []leftItem {
	items := make([]leftItem, 0, len(sessions))
	for i, r := range sessions {
		items = append(items, leftItem{issueIdx: i, subagentIdx: -1})
		if collapsed[r.Identifier] {
			continue
		}
		for j := range subagents[r.Identifier] {
			items = append(items, leftItem{
				issueIdx:    i,
				subagentIdx: j,
				label:       subagents[r.Identifier][j].description,
			})
		}
	}
	return items
}

// findNavItem returns the index in navItems that matches (identifier, subIdx).
// Falls back to 0 if not found.
func findNavItem(navItems []leftItem, sessions []server.RunningRow, id string, subIdx int) int {
	for i, item := range navItems {
		if item.issueIdx >= len(sessions) {
			continue
		}
		if sessions[item.issueIdx].Identifier == id && item.subagentIdx == subIdx {
			return i
		}
	}
	return 0
}

// pickerSlugAt returns the slug for row i in the picker list.
// Row 0 = "All issues" (""), row 1 = "No project" ("__no_project__"), row 2+ = project slugs.
func (m *Model) pickerSlugAt(i int) string {
	switch i {
	case 0:
		return "" // "All issues"
	case 1:
		return "__no_project__"
	default:
		idx := i - 2
		if idx < len(m.pickerProjects) {
			return m.pickerProjects[idx].Slug
		}
		return ""
	}
}

// renderSplitDivider renders the vertical divider between logs and details panes.
func (m *Model) renderSplitDivider() string {
	bh := m.bodyHeight()
	lines := make([]string, bh)
	for i := range lines {
		lines[i] = styleGray.Render("│")
	}
	return strings.Join(lines, "\n")
}

// renderSplitDetails builds the split details pane content showing runtime info
// and issue details for the selected session.
func (m *Model) renderSplitDetails() string {
	detailW := m.splitVP.Width
	detailStyle := lipgloss.NewStyle().Width(detailW).MaxWidth(detailW)

	selID, selKind := m.selectedSessionID()

	// Header
	detailHdrStyle := styleLabel
	detailHeader := "◆─"
	if m.activePanel == 2 {
		detailHeader = "●─"
		detailHdrStyle = styleGreen.Bold(true)
	}
	title := detailStyle.Render(detailHdrStyle.Render(detailHeader+"[ DETAILS ]") +
		styleMuted.Render("  ↑/↓ scroll"))
	sep := detailStyle.Render(styleGray.Render(strings.Repeat("━", detailW)))

	bh := m.bodyHeight()
	if selID == "" {
		var lines []string
		lines = append(lines, title, sep, detailStyle.Render(styleMuted.Render("  No session selected.")))
		for len(lines) < bh {
			lines = append(lines, detailStyle.Render(""))
		}
		return strings.Join(lines, "\n")
	}

	// Build content lines for the viewport
	var content []string
	addContent := func(s string) { content = append(content, s) }

	// Section 1: Runtime info
	switch selKind {
	case "running":
		if item, ok := m.currentNavItem(); ok && item.issueIdx < len(m.sessions) {
			r := m.sessions[item.issueIdx]
			sl := strings.ToLower(r.State)
			var stateRender string
			switch {
			case strings.Contains(sl, "running"), strings.Contains(sl, "progress"):
				stateRender = styleGreen.Render("◉ " + strings.ToUpper(r.State))
			case strings.Contains(sl, "review"), strings.Contains(sl, "done"):
				stateRender = styleCyan.Render("✓ " + strings.ToUpper(r.State))
			default:
				stateRender = styleMuted.Render("· " + strings.ToUpper(r.State))
			}
			addContent(stateRender)
			addContent("")
			if prof := m.profileOverrides[r.Identifier]; prof != "" {
				addContent(styleMuted.Render("Profile: ") + styleYellow.Render("⚙ "+prof))
			}
			addContent(styleMuted.Render("Turns: ") + fmt.Sprintf("%d", r.TurnCount))
			addContent(styleMuted.Render("Tokens: ") + fmt.Sprintf("↑%s ↓%s ∑%s",
				fmtCount(r.InputTokens), fmtCount(r.OutputTokens), fmtCount(r.InputTokens+r.OutputTokens)))
			addContent(styleMuted.Render("Backend: ") + r.Backend)
			if r.WorkerHost != "" {
				addContent(styleMuted.Render("Worker: ") + styleCyan.Render(r.WorkerHost))
			}
			elapsed := time.Duration(r.ElapsedMs) * time.Millisecond
			addContent(styleMuted.Render("Elapsed: ") + fmtDuration(elapsed))
			subs := m.subagents[r.Identifier]
			if len(subs) > 0 {
				addContent("")
				addContent(stylePurple.Render(fmt.Sprintf("Subagents (%d):", len(subs))))
				for _, sub := range subs {
					addContent(stylePurple.Faint(true).Render("  ↗ " + truncate(sub.description, detailW-6)))
				}
			}
		}
	case "paused":
		addContent(styleRed.Render("⏸ PAUSED"))
		addContent("")
		addContent(styleMuted.Render("Press 'r' to resume"))
		addContent(styleMuted.Render("Press 'D' to discard"))
	case "history":
		if m.historyCursor < len(m.history) {
			hi := len(m.history) - 1 - m.historyCursor
			h := m.history[hi]
			var statusRender string
			switch h.Status {
			case "succeeded":
				statusRender = styleGreen.Render("✓ SUCCEEDED")
			case "failed":
				statusRender = styleRed.Render("✗ FAILED")
			default:
				statusRender = styleMuted.Render("⊘ " + strings.ToUpper(h.Status))
			}
			addContent(statusRender)
			addContent("")
			addContent(styleMuted.Render("Turns: ") + fmt.Sprintf("%d", h.TurnCount))
			addContent(styleMuted.Render("Tokens: ") + fmt.Sprintf("↑%s ↓%s ∑%s",
				fmtCount(h.InputTokens), fmtCount(h.OutputTokens), fmtCount(h.TotalTokens)))
			addContent(styleMuted.Render("Backend: ") + h.Backend)
			if h.WorkerHost != "" {
				addContent(styleMuted.Render("Worker: ") + styleCyan.Render(h.WorkerHost))
			}
			elapsed := time.Duration(h.ElapsedMs) * time.Millisecond
			addContent(styleMuted.Render("Elapsed: ") + fmtDuration(elapsed))
		}
	}

	// Section 2: Issue details (from tracker)
	if m.splitIssueDetail != nil {
		detail := m.splitIssueDetail
		addContent("")
		addContent(styleGray.Render(strings.Repeat("─", detailW)))
		addContent("")

		// Title
		titleLines := wrapText(detail.Title, detailW-2)
		for _, line := range titleLines {
			addContent(styleCyan.Bold(true).Render(line))
		}
		addContent("")

		// Priority
		priText := "None"
		switch detail.Priority {
		case 1:
			priText = styleRed.Render("Urgent")
		case 2:
			priText = styleYellow.Render("High")
		case 3:
			priText = styleMuted.Render("Medium")
		case 4:
			priText = "Low"
		}
		addContent(styleMuted.Render("Priority: ") + priText)

		// Description
		if detail.Description != "" {
			addContent("")
			addContent(styleMuted.Render("Description:"))
			descLines := wrapText(detail.Description, detailW-2)
			for _, line := range descLines {
				addContent("  " + line)
			}
		}

		// Comments
		if len(detail.Comments) > 0 {
			addContent("")
			addContent(styleMuted.Render(fmt.Sprintf("Comments (%d):", len(detail.Comments))))
			for _, c := range detail.Comments {
				author := truncate(c.Author, 12)
				bodyLines := wrapText(c.Body, detailW-4)
				if len(bodyLines) > 0 {
					addContent("  " + styleCyan.Render(author+": ") + bodyLines[0])
					for _, bl := range bodyLines[1:] {
						addContent("    " + bl)
					}
				}
			}
		}
	}

	// Set content into viewport
	m.splitVP.SetContent(strings.Join(content, "\n"))

	return title + "\n" + sep + "\n" + m.splitVP.View()
}

// applyPickerFilter converts the current picker selection into a project filter
// and calls cfg.SetProjectFilter.
func (m *Model) applyPickerFilter() {
	if m.cfg.SetProjectFilter == nil {
		return
	}
	if m.pickerSel[""] {
		// "All issues" selected → clear filter.
		m.cfg.SetProjectFilter([]string{})
		return
	}
	var slugs []string
	for slug, on := range m.pickerSel {
		if on && slug != "" {
			slugs = append(slugs, slug)
		}
	}
	m.cfg.SetProjectFilter(slugs)
}

// renderProjectPicker replaces the left pane with an interactive project list.
func (m *Model) renderProjectPicker() string {
	bh := m.bodyHeight()
	leftStyle := lipgloss.NewStyle().Width(leftPaneWidth).MaxWidth(leftPaneWidth)
	var lines []string
	add := func(content string) {
		if len(lines) < bh {
			lines = append(lines, leftStyle.Render(content))
		}
	}

	add(styleBold.Render("▌ PROJECT FILTER") + styleGray.Render("  space toggle enter apply"))
	add(styleGray.Render(strings.Repeat("─", leftPaneWidth)))

	if m.pickerLoading {
		add(styleGray.Render("  Loading projects..."))
	} else {
		type pickerRow struct {
			slug  string
			label string
		}
		rows := []pickerRow{
			{slug: "", label: "All issues"},
			{slug: "__no_project__", label: "No project"},
		}
		for _, p := range m.pickerProjects {
			rows = append(rows, pickerRow{slug: p.Slug, label: p.Name})
		}

		for i, row := range rows {
			checked := "[ ]"
			if m.pickerSel[row.slug] {
				checked = "[✓]"
			}
			label := truncate(row.label, leftPaneWidth-7)
			content := fmt.Sprintf("  %s %s", checked, label)
			if i == m.pickerCursor {
				padded := fmt.Sprintf("%-*s", leftPaneWidth, content)
				if len(padded) > leftPaneWidth {
					padded = padded[:leftPaneWidth]
				}
				add(styleReverse.Render(padded))
			} else if row.slug == "" {
				add(styleCyan.Render(content))
			} else {
				add(content)
			}
		}
	}

	for len(lines) < bh {
		lines = append(lines, leftStyle.Render(""))
	}
	return strings.Join(lines, "\n")
}

// renderProfilePicker replaces the left pane with the agent profile assignment picker.
func (m *Model) renderProfilePicker() string {
	bh := m.bodyHeight()
	leftStyle := lipgloss.NewStyle().Width(leftPaneWidth).MaxWidth(leftPaneWidth)
	var lines []string
	add := func(content string) {
		if len(lines) < bh {
			lines = append(lines, leftStyle.Render(content))
		}
	}

	add(stylePurple.Bold(true).Render("▌ ASSIGN PROFILE") + styleGray.Render("  ↑↓ select  enter confirm"))
	add(styleGray.Render(strings.Repeat("─", leftPaneWidth)))
	add(styleMuted.Render("  Target: ") + styleCyan.Render(m.profilePickerTarget))
	add("")

	snap := m.snap()
	current := m.profileOverrides[m.profilePickerTarget]

	for i, name := range m.profilePickerItems {
		indicator := styleMuted.Render("○")
		if name == current {
			indicator = styleGreen.Render("●")
		}
		backend := ""
		if def, ok := snap.ProfileDefs[name]; ok && def.Backend != "" {
			backend = styleMuted.Render(" — " + def.Backend)
		}
		label := truncate(name, leftPaneWidth-10)
		content := fmt.Sprintf("  %s %s%s", indicator, label, backend)
		if i == m.profilePickerCursor {
			padded := fmt.Sprintf("%-*s", leftPaneWidth, content)
			if len(padded) > leftPaneWidth {
				padded = padded[:leftPaneWidth]
			}
			add(styleReverse.Render(padded))
		} else {
			add(content)
		}
	}

	// Separator + clear override row
	add(styleGray.Render(strings.Repeat("─", leftPaneWidth)))
	clearIdx := len(m.profilePickerItems)
	clearContent := "  " + styleRed.Render("✕") + " " + styleMuted.Render("clear override")
	if m.profilePickerCursor == clearIdx {
		padded := fmt.Sprintf("%-*s", leftPaneWidth, clearContent)
		if len(padded) > leftPaneWidth {
			padded = padded[:leftPaneWidth]
		}
		add(styleReverse.Render(padded))
	} else {
		add(clearContent)
	}

	for len(lines) < bh {
		lines = append(lines, leftStyle.Render(""))
	}
	return strings.Join(lines, "\n")
}

// isTodoState checks if a state is a TODO (active) state.
func (m *Model) isTodoState(state string) bool {
	for _, s := range m.cfg.TodoStates {
		if strings.EqualFold(s, state) {
			return true
		}
	}
	return false
}

// isBacklogState checks if a state is a BACKLOG state.
func (m *Model) isBacklogState(state string) bool {
	for _, s := range m.cfg.BacklogStates {
		if strings.EqualFold(s, state) {
			return true
		}
	}
	return false
}

// renderBacklogPanel replaces the left pane with the interactive backlog issue list.
// Shows BACKLOG items first, then ACTIVE items (configured active states) in separate sections.
func (m *Model) renderBacklogPanel() string {
	bh := m.bodyHeight()
	leftStyle := lipgloss.NewStyle().Width(leftPaneWidth).MaxWidth(leftPaneWidth)
	var lines []string
	add := func(content string) {
		if len(lines) < bh {
			lines = append(lines, leftStyle.Render(content))
		}
	}

	// Split items into ACTIVE and BACKLOG sections.
	var todoItems, backlogItems []BacklogIssueItem
	for _, item := range m.backlogItems {
		if m.isTodoState(item.State) {
			todoItems = append(todoItems, item)
		} else {
			backlogItems = append(backlogItems, item)
		}
	}

	// Cursor position calculation:
	// 0..len(backlogItems)-1 = BACKLOG section (first)
	// len(backlogItems)..len(backlogItems)+len(todoItems)-1 = ACTIVE section (second)
	backlogCount := len(backlogItems)

	// Header - single line with key hints
	add(styleLabel.Render("◆─[ BACKLOG & ACTIVE ]") + " " + styleMuted.Render("↑↓ nav  d dispatch  esc close"))

	switch {
	case m.backlogLoading:
		add(styleGray.Render(strings.Repeat("━", leftPaneWidth)))
		add(styleMuted.Render("  · loading..."))
	case m.backlogErr != "":
		add(styleGray.Render(strings.Repeat("━", leftPaneWidth)))
		add(styleRed.Render("  ✗ " + truncate(m.backlogErr, leftPaneWidth-4)))
	default:
		// BACKLOG section (first)
		add(styleGray.Render(strings.Repeat("━", leftPaneWidth)))
		add(styleMuted.Render("◆─ BACKLOG (needs planning)"))
		if len(backlogItems) == 0 {
			add(styleMuted.Render("  ○ backlog is empty"))
		} else {
			for i, item := range backlogItems {
				if len(lines) >= bh-3 { // Reserve space for ACTIVE section header
					add(styleMuted.Render("  ..."))
					break
				}
				m.renderBacklogItem(item, i == m.backlogCursor, add)
			}
		}

		if len(lines) >= bh-2 {
			goto done
		}

		// ACTIVE section (second)
		add("")
		add(styleMuted.Render("●─ ACTIVE (ready for work)"))
		if len(todoItems) == 0 {
			add(styleMuted.Render("  ○ no active items"))
		} else {
			for i, item := range todoItems {
				if len(lines) >= bh {
					break
				}
				// Cursor in ACTIVE section: offset by backlogCount
				cursorIdx := backlogCount + i
				m.renderBacklogItem(item, cursorIdx == m.backlogCursor, add)
			}
		}
	}

done:
	for len(lines) < bh {
		lines = append(lines, leftStyle.Render(""))
	}
	return strings.Join(lines, "\n")
}

// renderBacklogItem renders a single backlog/todo item.
func (m *Model) renderBacklogItem(item BacklogIssueItem, selected bool, add func(string)) {
	// Priority symbol (1 char)
	priSym := "·"
	switch item.Priority {
	case 1:
		priSym = "!"
	case 2:
		priSym = "▲"
	case 3:
		priSym = "●"
	}

	// Truncate fields to fit - no state column, just ID and title
	idTrunc := truncate(item.Identifier, 12)
	titleTrunc := truncate(item.Title, leftPaneWidth-18) // Reserve space for prefix and ID

	if selected {
		// Selected row with highlight
		row := fmt.Sprintf("▶ %s %-12s %s", priSym, idTrunc, titleTrunc)
		add(styleCyan.Bold(true).Render(row))
	} else {
		// Normal row
		row := fmt.Sprintf("  %s %-12s %s", priSym, idTrunc, titleTrunc)
		add(styleMuted.Render(row))
	}
}

// renderBacklogDetails shows details of the selected backlog item in the right pane.
func (m *Model) renderBacklogDetails() string {
	rightW := m.logVP.Width
	rightStyle := lipgloss.NewStyle().Width(rightW).MaxWidth(rightW)
	bh := m.bodyHeight()

	if m.backlogCursor < 0 || m.backlogCursor >= len(m.backlogItems) {
		var lines []string
		for len(lines) < bh {
			lines = append(lines, rightStyle.Render(""))
		}
		return strings.Join(lines, "\n")
	}

	item := m.backlogItems[m.backlogCursor]

	var lines []string
	add := func(content string) {
		if len(lines) < bh {
			lines = append(lines, rightStyle.Render(content))
		}
	}

	// Header with key hint
	add(styleLabel.Render("●─[ ISSUE DETAILS ]") + " " + styleMuted.Render("d dispatch  b close"))
	add(styleGray.Render(strings.Repeat("━", rightW)))
	add("")

	// Identifier and state
	add(styleCyan.Bold(true).Render(item.Identifier) + " " + styleMuted.Render("["+item.State+"]"))
	add("")

	// Priority
	priText := "No priority"
	switch item.Priority {
	case 1:
		priText = styleRed.Render("Urgent")
	case 2:
		priText = styleYellow.Render("High")
	case 3:
		priText = styleMuted.Render("Medium")
	case 4:
		priText = "Low"
	}
	add(styleMuted.Render("Priority: ") + priText)
	add("")

	// Title
	add(styleMuted.Render("Title:"))
	titleLines := wrapText(item.Title, rightW-2)
	for _, line := range titleLines {
		if len(lines) >= bh-2 {
			break
		}
		add("  " + line)
	}
	add("")

	// Description
	if item.Description != "" {
		add(styleMuted.Render("Description:"))
		descLines := wrapText(item.Description, rightW-2)
		maxDescLines := bh - len(lines) - 4 // Reserve space for comments
		for i, line := range descLines {
			if i >= maxDescLines {
				add("  ...")
				break
			}
			add("  " + line)
		}
		add("")
	}

	// Comments
	if len(item.Comments) > 0 && len(lines) < bh-2 {
		add(styleMuted.Render(fmt.Sprintf("Comments (%d):", len(item.Comments))))
		maxComments := bh - len(lines) - 1
		commentCount := 0
		for _, c := range item.Comments {
			if commentCount >= maxComments {
				add(styleMuted.Render("  ..."))
				break
			}
			author := truncate(c.Author, 12)
			add(styleCyan.Render(author+":") + " " + truncate(c.Body, rightW-16))
			commentCount++
		}
	}

	// Fill remaining space
	for len(lines) < bh {
		lines = append(lines, rightStyle.Render(""))
	}

	return strings.Join(lines, "\n")
}

// wrapText wraps text to the given width, returning lines.
func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{}
	}

	var lines []string
	var currentLine strings.Builder

	for _, word := range words {
		if currentLine.Len() == 0 {
			currentLine.WriteString(word)
		} else if currentLine.Len()+1+len(word) <= width {
			currentLine.WriteString(" ")
			currentLine.WriteString(word)
		} else {
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLine.WriteString(word)
		}
	}
	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}

	return lines
}

// ganttEntry is a unified view of a running, paused, or completed session for Gantt rendering.
type ganttEntry struct {
	identifier string
	startedAt  time.Time
	elapsedMs  int64
	turnCount  int
	tokens     int
	isHistory  bool
	isPaused   bool
	status     string // "succeeded" | "failed" | "cancelled" (history only)
}

// renderGantt renders the Gantt timeline section below the split panes.
//
// Normal mode (issues tab): shows all running + paused sessions side by side.
// History mode (history tab): shows all runs for the selected issue, j/k cursor.
func (m *Model) renderGantt() string {
	fullW := m.width

	var entries []ganttEntry

	if m.leftTab != "history" {
		// ── Normal mode: all running sessions + paused ────────────────────────
		for _, r := range m.sessions {
			entries = append(entries, ganttEntry{
				identifier: r.Identifier,
				startedAt:  r.StartedAt,
				elapsedMs:  r.ElapsedMs,
				turnCount:  r.TurnCount,
				tokens:     r.InputTokens + r.OutputTokens,
			})
		}
		// Paused sessions: look up most recent history run for elapsed data.
		for _, pausedID := range m.paused {
			// Already shown as running? (shouldn't happen, but guard)
			found := false
			for _, e := range entries {
				if e.identifier == pausedID {
					found = true
					break
				}
			}
			if found {
				continue
			}
			entry := ganttEntry{identifier: pausedID, isPaused: true, startedAt: time.Now()}
			for i := len(m.history) - 1; i >= 0; i-- {
				if m.history[i].Identifier == pausedID {
					h := m.history[i]
					entry.startedAt = h.StartedAt
					entry.elapsedMs = h.ElapsedMs
					entry.turnCount = h.TurnCount
					entry.tokens = h.TotalTokens
					break
				}
			}
			entries = append(entries, entry)
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].startedAt.After(entries[j].startedAt)
		})
		if len(entries) > maxGanttBars {
			entries = entries[:maxGanttBars]
		}
	} else {
		// ── History mode: all runs for the selected issue ──────────────────────
		selID, _ := m.selectedSessionID()
		if selID == "" && len(m.history) > 0 {
			selID = m.history[len(m.history)-1].Identifier
		}
		for _, r := range m.sessions {
			if r.Identifier == selID {
				entries = append(entries, ganttEntry{
					identifier: r.Identifier,
					startedAt:  r.StartedAt,
					elapsedMs:  r.ElapsedMs,
					turnCount:  r.TurnCount,
					tokens:     r.InputTokens + r.OutputTokens,
				})
			}
		}
		for _, h := range m.history {
			if h.Identifier == selID {
				entries = append(entries, ganttEntry{
					identifier: h.Identifier,
					startedAt:  h.StartedAt,
					elapsedMs:  h.ElapsedMs,
					turnCount:  h.TurnCount,
					tokens:     h.TotalTokens,
					isHistory:  true,
					status:     h.Status,
				})
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].startedAt.After(entries[j].startedAt)
		})
	}

	if fullW < 40 || len(entries) == 0 {
		return ""
	}

	// ── Scrollable window ────────────────────────────────────────────────────
	// In history mode the full run list can exceed maxGanttBars. Compute a
	// sliding window that keeps m.timelineCursor visible. In normal mode we
	// always show the first maxGanttBars entries (already sorted by recency).
	totalEntries := len(entries)
	ganttOffset := 0
	if m.leftTab == "history" && totalEntries > maxGanttBars {
		// Scroll only when panel 2 is focused and cursor has moved past the window.
		if m.activePanel == 2 {
			ganttOffset = max(m.timelineCursor-maxGanttBars+1, 0)
			if ganttOffset+maxGanttBars > totalEntries {
				ganttOffset = totalEntries - maxGanttBars
			}
		}
	}
	if len(entries) > maxGanttBars {
		end := min(ganttOffset+maxGanttBars, totalEntries)
		entries = entries[ganttOffset:end]
	}

	// Scale bars proportionally to elapsed time. Running sessions use live elapsed.
	now := time.Now()
	var maxElapsedMs int64
	for i, e := range entries {
		el := e.elapsedMs
		if !e.isHistory {
			if el2 := now.Sub(e.startedAt).Milliseconds(); el2 > 0 {
				el = el2
			}
			entries[i].elapsedMs = el
		}
		if el > maxElapsedMs {
			maxElapsedMs = el
		}
	}
	if maxElapsedMs < 60_000 { // minimum 1-minute scale
		maxElapsedMs = 60_000
	}

	const labelW = 13
	const elapsedW = 7
	barW := max(10, fullW-labelW-elapsedW-6) // 6 for leading spaces and separators

	var sb strings.Builder

	// Scroll indicators for history mode when there are off-screen entries.
	scrollHint := ""
	if m.leftTab == "history" && totalEntries > maxGanttBars {
		above := ganttOffset
		below := totalEntries - (ganttOffset + len(entries))
		switch {
		case above > 0 && below > 0:
			scrollHint = styleMuted.Render(fmt.Sprintf("  ↑%d ↓%d", above, below))
		case above > 0:
			scrollHint = styleMuted.Render(fmt.Sprintf("  ↑%d above", above))
		case below > 0:
			scrollHint = styleMuted.Render(fmt.Sprintf("  ↓%d more", below))
		}
	}

	// Title + separator rule.
	timelineHeader := "◆─[ TIMELINE ]"
	timelineHdrStyle := styleLabel
	timelineHint := ""
	if m.activePanel == 2 {
		timelineHeader = "●─[ TIMELINE ]"
		timelineHdrStyle = styleGreen.Bold(true)
		if m.timelineDetail {
			timelineHint = styleMuted.Render("  esc:back")
		} else if m.leftTab == "history" {
			timelineHint = styleMuted.Render("  ↑/↓:select · enter:phases · esc:back")
		} else {
			timelineHint = styleMuted.Render("  enter:phases · esc:back")
		}
	} else {
		timelineHint = styleMuted.Render("  tab to focus")
	}
	ruleLen := max(0, fullW-15-xansi.StringWidth(timelineHint)-xansi.StringWidth(scrollHint))
	rule := styleGray.Render(strings.Repeat("━", ruleLen))
	sb.WriteString(timelineHdrStyle.Render(timelineHeader) + timelineHint + scrollHint + rule + "\n")

	// In normal mode: highlight bar matching the left-panel selected issue.
	// In history mode: highlight bar at timelineCursor (absolute index).
	var normalSelID string
	if m.leftTab != "history" {
		normalSelID, _ = m.selectedSessionID()
		if normalSelID == "" && len(m.sessions) > 0 {
			normalSelID = m.sessions[0].Identifier
		}
	}

	for rowIdx, e := range entries {
		var isCursorRow bool
		if m.leftTab == "history" {
			// rowIdx is relative to ganttOffset; compare against absolute timelineCursor.
			isCursorRow = m.activePanel == 2 && (rowIdx+ganttOffset) == m.timelineCursor
		} else if m.activePanel == 2 {
			isCursorRow = rowIdx == m.timelineCursor
		} else {
			isCursorRow = e.identifier == normalSelID
		}

		// All bars start at the left edge; width is proportional to elapsed time.
		startPos := 0
		endPos := max(min(int(int64(barW)*e.elapsedMs/maxElapsedMs), barW), 1)
		sessionWidth := endPos - startPos

		// Build bar as rune slice.
		barRunes := make([]rune, barW)

		// Both running and history use solid blocks; ░ padding shows unused span.
		for i := range barRunes {
			if i >= startPos && i < endPos {
				barRunes[i] = '█'
			} else {
				barRunes[i] = '░'
			}
		}
		if e.isHistory {
			// Status glyph at bar end for completed sessions.
			statusGlyph := '✓'
			switch e.status {
			case "failed":
				statusGlyph = '✗'
			case "cancelled":
				statusGlyph = '⊘'
			}
			if endPos > startPos && endPos-1 < barW {
				barRunes[endPos-1] = statusGlyph
			}
		} else {
			// Subagent tick marks for running sessions.
			totalLines := 0
			if m.buf != nil {
				totalLines = len(m.buf.Get(e.identifier))
			}
			if totalLines > 0 && sessionWidth > 0 {
				for _, sub := range m.subagents[e.identifier] {
					tickRelPos := int(int64(sessionWidth) * int64(sub.startLine) / int64(totalLines))
					absPos := startPos + tickRelPos
					if absPos > startPos && absPos < endPos {
						barRunes[absPos] = '│'
					}
				}
			}
		}

		// Embed inline label inside the filled region (both running and history).
		barLabel := []rune(fmt.Sprintf(" t%d · %s", e.turnCount, fmtCount(e.tokens)))
		if len(barLabel) < sessionWidth-1 {
			for j, c := range barLabel {
				pos := startPos + j
				if pos < endPos {
					barRunes[pos] = c
				}
			}
		}

		elapsed := time.Duration(e.elapsedMs) * time.Millisecond
		elapsedStr := fmt.Sprintf("%-*s", elapsedW, fmtDuration(elapsed))

		// Label prefix: cursor mark always shown on the highlighted bar.
		cursorMark := "  "
		if isCursorRow {
			cursorMark = "► "
		}
		var rowLabel string
		if m.leftTab == "history" {
			// History mode: same issue, label by date.
			rowLabel = fmt.Sprintf("%-*s", labelW, truncate(e.startedAt.Format("01/02 15:04"), labelW))
		} else {
			// Normal mode: label by identifier; paused gets ⏸ prefix.
			if e.isPaused {
				rowLabel = fmt.Sprintf("%-*s", labelW, truncate("⏸ "+e.identifier, labelW))
			} else {
				rowLabel = fmt.Sprintf("%-*s", labelW, truncate(e.identifier, labelW))
			}
		}

		var barStyle lipgloss.Style
		if isCursorRow {
			barStyle = styleCyan.Bold(true)
		} else if e.isPaused {
			barStyle = styleYellow.Faint(true)
		} else if e.isHistory {
			switch e.status {
			case "failed":
				barStyle = styleRed.Faint(true)
			case "cancelled":
				barStyle = styleMuted
			default: // succeeded
				barStyle = styleGreen.Faint(true)
			}
		} else {
			barStyle = styleGreen.Faint(true)
		}

		sb.WriteString(
			styleMuted.Render(cursorMark+rowLabel+" ") +
				barStyle.Render(string(barRunes)) +
				styleMuted.Render(" "+elapsedStr) +
				"\n",
		)

		// Phase drill-down: shown for the cursor row when timelineDetail is active.
		// Phase Gantt: one row per subagent, each with its own proportional bar.
		if isCursorRow && m.timelineDetail {
			// For live sessions, subagents come from the running map.
			// For history/paused, extract directly from the log buffer.
			subs := m.subagents[e.identifier]
			var logLines []string
			if m.buf != nil {
				logLines = m.buf.Get(e.identifier)
			}
			if len(subs) == 0 && len(logLines) > 0 {
				subs = extractSubagents(logLines)
			}
			totalLines := len(logLines)
			if len(subs) == 0 || totalLines == 0 {
				noPhaseLabel := fmt.Sprintf("%-*s", labelW, "phases")
				sb.WriteString(styleMuted.Render("  "+noPhaseLabel+" ") + styleMuted.Render("no subagent phases yet") + "\n")
			} else {
				phaseBarColors := []lipgloss.Color{
					"#00ff88", "#00d4ff", "#bf5af2", "#ffb000", "#ff4040", "#7dd3fc",
				}
				const maxPhaseRows = 8
				for si, sub := range subs {
					if si >= maxPhaseRows {
						overflow := fmt.Sprintf("%-*s  +%d more phases…", labelW, "", len(subs)-maxPhaseRows)
						sb.WriteString(styleMuted.Render("  "+overflow) + "\n")
						break
					}
					color := phaseBarColors[si%len(phaseBarColors)]
					phaseStyle := lipgloss.NewStyle().Foreground(color)

					// Compute active region in bar coordinates.
					subStartPos := int(int64(barW) * int64(sub.startLine) / int64(totalLines))
					subEnd := min(sub.endLine, totalLines)
					subEndPos := int(int64(barW) * int64(subEnd) / int64(totalLines))
					if subEndPos <= subStartPos {
						subEndPos = subStartPos + 1
					}
					subEndPos = min(subEndPos, barW)

					// Build bar: ░ before active region, █ during, ░ after.
					phaseRunes := make([]rune, barW)
					for i := range phaseRunes {
						if i >= subStartPos && i < subEndPos {
							phaseRunes[i] = '█'
						} else {
							phaseRunes[i] = '░'
						}
					}

					// Approximate phase duration.
					phaseMs := e.elapsedMs * int64(subEnd-sub.startLine) / int64(totalLines)
					elStr := fmt.Sprintf("%-*s", elapsedW, fmtDuration(time.Duration(phaseMs)*time.Millisecond))

					descLabel := fmt.Sprintf("[%d] %s", si+1, sub.description)
					rowLbl := fmt.Sprintf("%-*s", labelW, truncate(descLabel, labelW))
					sb.WriteString(
						styleMuted.Render("  "+rowLbl+" ") +
							phaseStyle.Render(string(phaseRunes)) +
							styleMuted.Render(" "+elStr) +
							"\n",
					)
				}
			}
		}
	}

	// Duration axis: elapsed-time labels at 0%, 50%, 100% of the bar width.
	axis := buildDurationAxis(labelW+3, barW, maxElapsedMs)
	sb.WriteString(styleGray.Render(axis))

	return sb.String()
}

// buildDurationAxis returns a string with elapsed-duration labels at the start,
// midpoint, and end of the bar region (e.g. "0s", "2m30s", "5m01s").
func buildDurationAxis(offset, barW int, maxElapsedMs int64) string {
	buf := []rune(strings.Repeat(" ", offset+barW+12))
	type mark struct {
		pos   int
		label string
	}
	marks := []mark{
		{offset, "0s"},
		{offset + barW/2, fmtDuration(time.Duration(maxElapsedMs/2) * time.Millisecond)},
		{offset + barW, fmtDuration(time.Duration(maxElapsedMs) * time.Millisecond)},
	}
	for _, mk := range marks {
		for i, c := range mk.label {
			if mk.pos+i < len(buf) {
				buf[mk.pos+i] = c
			}
		}
	}
	return strings.TrimRight(string(buf), " ")
}

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	mn := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, mn, s)
	}
	if mn > 0 {
		return fmt.Sprintf("%dm%ds", mn, s)
	}
	return fmt.Sprintf("%ds", s)
}

func fmtCount(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return s[:max-1] + "…"
}

// osc8Link wraps text in an OSC 8 terminal hyperlink escape sequence.
// Supported by iTerm2, WezTerm, Kitty, GNOME Terminal ≥ 3.26, etc.
func osc8Link(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// extractPRLink scans log lines for a PR URL written by the orchestrator
// when it detects or creates an open pull request.
func extractPRLink(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		e, ok := parseBufLine(lines[i])
		if !ok {
			continue
		}
		if e.Msg == "worker: pr_opened" && e.URL != "" {
			return e.URL
		}
	}
	return ""
}

var copyURLToClipboardFn = copyURLToClipboard
var clipboardGOOS = runtime.GOOS
var copyURLToNativeClipboardFn = copyURLToNativeClipboard
var writeOSC52ClipboardFn = writeOSC52Clipboard

// copyURLToClipboard copies a URL to the system clipboard.
//
// On macOS we prefer `pbcopy`, which talks to the OS clipboard directly and
// works in terminals that ignore OSC 52. Other platforms fall back to OSC 52.
func copyURLToClipboard(url string) {
	if clipboardGOOS == "darwin" {
		if err := copyURLToNativeClipboardFn(url); err == nil {
			return
		}
	}
	writeOSC52ClipboardFn(url)
}

func copyURLToNativeClipboard(url string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(url)
	return cmd.Run()
}

func writeOSC52Clipboard(url string) {
	encoded := base64.StdEncoding.EncodeToString([]byte(url))
	// OSC 52 format: ESC ] 52 ; c ; <base64> ST
	// Using BEL (\x07) as the string terminator for broadest compat.
	_, _ = os.Stderr.WriteString("\x1b]52;c;" + encoded + "\x07")
}
