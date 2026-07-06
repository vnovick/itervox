package statusui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	ListUp        key.Binding
	ListDown      key.Binding
	Toggle        key.Binding
	LogUp         key.Binding
	LogDown       key.Binding
	Kill          key.Binding
	Quit          key.Binding
	OpenPicker    key.Binding
	PickerSel     key.Binding
	PickerApply   key.Binding
	PickerClose   key.Binding
	WorkersUp     key.Binding
	WorkersDown   key.Binding
	BacklogToggle key.Binding
	Dispatch      key.Binding
	Resume        key.Binding
	Terminate     key.Binding
	PanelNext     key.Binding
	EscKey        key.Binding
	DrillDown     key.Binding
	OpenURL       key.Binding
	OpenWebUI     key.Binding
	HistoryTab    key.Binding
	AssignProfile key.Binding
	SplitToggle   key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		ListUp: key.NewBinding(
			key.WithKeys("up"),
			key.WithHelp("↑", "prev"),
		),
		ListDown: key.NewBinding(
			key.WithKeys("down"),
			key.WithHelp("↓", "next"),
		),
		Toggle: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("spc", "expand/collapse"),
		),
		LogUp: key.NewBinding(
			key.WithKeys("k"),
			key.WithHelp("k", "log page up"),
		),
		LogDown: key.NewBinding(
			key.WithKeys("j"),
			key.WithHelp("j", "log page dn"),
		),
		Kill: key.NewBinding(
			key.WithKeys("x"),
			key.WithHelp("x", "pause"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
		OpenPicker: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "project filter"),
		),
		PickerSel: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "toggle"),
		),
		PickerApply: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "apply"),
		),
		PickerClose: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "close"),
		),
		PanelNext: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "focus panel"),
		),
		EscKey: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "back"),
		),
		DrillDown: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "drill down"),
		),
		WorkersUp: key.NewBinding(
			key.WithKeys("+", "="), // '=' is same physical key as '+' without shift on some setups
			key.WithHelp("+", "more workers"),
		),
		WorkersDown: key.NewBinding(
			key.WithKeys("-", "_"),
			key.WithHelp("-", "fewer workers"),
		),
		BacklogToggle: key.NewBinding(
			key.WithKeys("b"),
			key.WithHelp("b", "backlog"),
		),
		Dispatch: key.NewBinding(
			key.WithKeys("d", "enter"),
			key.WithHelp("d/enter", "dispatch/details"),
		),
		Resume: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "resume paused"),
		),
		Terminate: key.NewBinding(
			key.WithKeys("D"),
			key.WithHelp("D", "discard paused"),
		),
		OpenURL: key.NewBinding(
			key.WithKeys("o"),
			key.WithHelp("o", copyPRHelpLabel),
		),
		OpenWebUI: key.NewBinding(
			key.WithKeys("w"),
			key.WithHelp("w", copyWebHelpLabel),
		),
		HistoryTab: key.NewBinding(
			key.WithKeys("h"),
			key.WithHelp("h", "history"),
		),
		AssignProfile: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "assign profile"),
		),
		SplitToggle: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "split details"),
		),
	}
}

// ShortHelp implements key.Map and returns the compact help binding list.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.ListUp, k.ListDown, k.PanelNext, k.DrillDown, k.EscKey, k.LogUp, k.LogDown, k.Kill, k.Resume, k.Terminate, k.WorkersUp, k.WorkersDown, k.BacklogToggle, k.Dispatch, k.OpenPicker, k.OpenURL, k.OpenWebUI, k.AssignProfile, k.SplitToggle, k.Quit}
}

// FullHelp implements key.Map and returns the full help binding list.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.ListUp, k.ListDown, k.Toggle, k.PanelNext, k.EscKey, k.DrillDown, k.LogUp, k.LogDown, k.Kill, k.Resume, k.Terminate, k.WorkersUp, k.WorkersDown, k.BacklogToggle, k.Dispatch, k.OpenPicker, k.AssignProfile, k.SplitToggle, k.Quit}}
}
