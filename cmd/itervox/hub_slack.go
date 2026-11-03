package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/joho/godotenv"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/hub"
	"github.com/vnovick/itervox/internal/hub/slack"
	"github.com/vnovick/itervox/internal/logging"
)

// The hub's Slack channel (#230) in the daemon, and `itervox hub slack`.
//
// The tokens are secrets, not settings: they live in ~/.itervox/hub/.env
// (0600, written by `itervox hub slack setup` or `itervox secret set KEY
// --hub`), are registered for log redaction the moment they are read and
// are never printed. hub.slack.* are load-time settings of the daemon that
// serves the hub (usually the user layer, ~/.itervox/settings.jsonc).

// Environment keys of the Slack tokens in ~/.itervox/hub/.env (or the
// daemon's environment).
const (
	slackAppTokenKey = "ITERVOX_SLACK_APP_TOKEN"
	slackBotTokenKey = "ITERVOX_SLACK_BOT_TOKEN"
)

// hubSlackKeyFile is the hub-local signing key of Slack actions.
const hubSlackKeyFile = "slack_actions.key"

// hubEnvPath is ~/.itervox/hub/.env, the hub's own secrets file.
func hubEnvPath() (string, error) {
	dir, err := hubStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".env"), nil
}

// hubSlackTokens reads the Slack tokens: ~/.itervox/hub/.env first, then
// the process environment. Both are registered for redaction.
func hubSlackTokens() (app, bot string) {
	var file map[string]string
	if path, err := hubEnvPath(); err == nil {
		if m, rerr := godotenv.Read(path); rerr == nil {
			file = m
		} else if !errors.Is(rerr, os.ErrNotExist) {
			slog.Warn("hub: slack: the hub secrets file does not parse", "path", path)
		}
	}
	get := func(k string) string {
		v := strings.TrimSpace(file[k])
		if v == "" {
			v = strings.TrimSpace(os.Getenv(k))
		}
		if v != "" {
			logging.RegisterSecret(v)
		}
		return v
	}
	return get(slackAppTokenKey), get(slackBotTokenKey)
}

// hubChannels builds the notification channels set up besides desktop:
// Slack when hub.slack.enabled and both tokens are present. A problem is
// logged (never a token) and leaves Slack out; the hub runs anyway.
func hubChannels(cfg *config.Config, stateDir string) []hub.Channel {
	sc := cfg.Hub.Slack
	if !sc.Enabled {
		return nil
	}
	app, bot := hubSlackTokens()
	if app == "" || bot == "" {
		slog.Warn("hub: slack: hub.slack.enabled is set but a token is missing; run `itervox hub slack setup`")
		return nil
	}
	ch, err := slack.New(slack.Config{
		AppToken: slack.Secret(app), BotToken: slack.Secret(bot),
		AllowedUsers: sc.AllowedUsers, Channels: sc.Channels, DefaultChannel: sc.DefaultChannel,
		KeyPath: filepath.Join(stateDir, hubSlackKeyFile),
	})
	if err != nil {
		slog.Warn("hub: slack: not started", "error", err)
		return nil
	}
	if len(sc.AllowedUsers) == 0 {
		slog.Info("hub: slack: hub.slack.allowed_users is empty, so Slack messages are alerts only")
	}
	return []hub.Channel{ch}
}

// hubSlackCmd runs `itervox hub slack setup|status` and returns the exit
// code.
func hubSlackCmd(args []string, in io.Reader, out, errOut io.Writer) int {
	usage := "usage: itervox hub slack setup [--json] [--manifest-only]\n       itervox hub slack status [--json]"
	if len(args) == 0 || (args[0] != "setup" && args[0] != "status") {
		_, _ = fmt.Fprintln(errOut, usage)
		return 2
	}
	fs := flag.NewFlagSet("hub slack "+args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	asJSON := fs.Bool("json", false, "setup: print the manifest as JSON; status: print JSON")
	manifestOnly := fs.Bool("manifest-only", false, "setup: print the app manifest and stop")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		_, _ = fmt.Fprintln(errOut, usage)
		return 2
	}
	if args[0] == "status" {
		return hubSlackStatus(*asJSON, out, errOut)
	}
	return hubSlackSetup(*asJSON, *manifestOnly, in, out, errOut)
}

// hubSlackSetup prints the app manifest and the steps to create the app,
// then reads the two tokens (echo off) into ~/.itervox/hub/.env and the
// allowed users and per-project channels into the user settings layer.
func hubSlackSetup(asJSON, manifestOnly bool, in io.Reader, out, errOut io.Writer) int {
	if err := slack.ValidateManifest(slack.Manifest()); err != nil { // never print a manifest Slack would refuse
		_, _ = fmt.Fprintf(errOut, "itervox hub slack setup: %v\n", err)
		return 1
	}
	manifest := slack.ManifestYAML()
	format := "YAML"
	if asJSON {
		manifest, format = slack.ManifestJSON(), "JSON"
	}
	if manifestOnly {
		_, _ = fmt.Fprint(out, manifest)
		return 0
	}
	_, _ = fmt.Fprintf(out, `Itervox hub: Slack through your own Slack app (Socket Mode, no inbound port).

1. Open https://api.slack.com/apps, choose Create New App > From a manifest,
   pick your workspace and paste this %s manifest:

%s
2. Basic Information > App-Level Tokens > Generate Token and Scopes: add the
   connections:write scope and copy the token (xapp-...).
3. Install App > Install to Workspace, then copy the Bot User OAuth Token
   (xoxb-...).
4. Invite the app to each channel it should post in (/invite @Itervox).

`, format, manifest)
	envPath, err := hubEnvPath()
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox hub slack setup: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox hub slack setup: %v\n", err)
		return 1
	}
	rd := bufio.NewReader(in)
	for _, tk := range []struct{ key, prefix, what string }{
		{slackAppTokenKey, slack.AppTokenPrefix, "App-level token"},
		{slackBotTokenKey, slack.BotTokenPrefix, "Bot token"},
	} {
		v, err := readHidden(tk.what+" ("+tk.prefix+"…)", rd, errOut)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "itervox hub slack setup: %v\n", err)
			return 1
		}
		if !strings.HasPrefix(v, tk.prefix) {
			_, _ = fmt.Fprintf(errOut, "itervox hub slack setup: the %s must start with %s; nothing saved\n", strings.ToLower(tk.what), tk.prefix)
			return 1
		}
		logging.RegisterSecret(v)
		if err := setEnvSecret(envPath, tk.key, v, true); err != nil {
			_, _ = fmt.Fprintf(errOut, "itervox hub slack setup: %v\n", err)
			return 1
		}
	}
	_, _ = fmt.Fprintf(out, "Saved %s and %s to %s (values not shown).\n\n", slackAppTokenKey, slackBotTokenKey, envPath)

	settings := []config.SettingAssignment{{Key: "hub.slack.enabled", Value: true}}
	users := splitIDs(readLine("Slack user IDs that may reply, approve and steer (U…, comma-separated; Enter for none: alerts only)", rd, out))
	for _, u := range users {
		if !slack.ValidID(u) || (u[0] != 'U' && u[0] != 'W') {
			_, _ = fmt.Fprintf(errOut, "itervox hub slack setup: %q is not a Slack user ID (Profile > ... > Copy member ID)\n", u)
			return 1
		}
	}
	settings = append(settings, config.SettingAssignment{Key: "hub.slack.allowed_users", Value: toAnySlice(users)})
	if def := readLine("Default channel ID for every project (C… or U… for a DM; Enter to skip)", rd, out); def != "" {
		if !slack.ValidID(def) {
			_, _ = fmt.Fprintf(errOut, "itervox hub slack setup: %q is not a Slack channel or user ID\n", def)
			return 1
		}
		settings = append(settings, config.SettingAssignment{Key: "hub.slack.default_channel", Value: def})
	}
	channels := map[string]any{}
	if regDir, err := daemonRegistryDir(); err == nil {
		members := hubMembersIn(regDir)
		names := hub.ProjectNames(members)
		var projects []string
		for _, n := range names {
			projects = append(projects, n)
		}
		slices.Sort(projects)
		projects = slices.Compact(projects)
		for _, p := range projects {
			ch := readLine("Channel ID for project "+p+" (Enter: the default channel)", rd, out)
			if ch == "" {
				continue
			}
			if !slack.ValidID(ch) {
				_, _ = fmt.Fprintf(errOut, "itervox hub slack setup: %q is not a Slack channel or user ID\n", ch)
				return 1
			}
			channels[p] = ch
		}
	}
	if len(channels) > 0 {
		settings = append(settings, config.SettingAssignment{Key: "hub.slack.channels", Value: channels})
	}
	path := config.UserSettingsPath()
	if path == "" {
		_, _ = fmt.Fprintln(errOut, "itervox hub slack setup: the user settings layer is off (ITERVOX_USER_SETTINGS=off); set hub.slack.* in a settings file yourself")
		return 1
	}
	if err := config.SetSettings(path, settings...); err != nil {
		_, _ = fmt.Fprintf(errOut, "itervox hub slack setup: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "Saved hub.slack settings to %s.\nRestart the daemon that serves the hub (see `itervox hub`) to connect; check with `itervox hub slack status`.\n", path)
	if len(users) == 0 {
		_, _ = fmt.Fprintln(out, "No allowed users: Slack messages are alerts only until you set hub.slack.allowed_users.")
	}
	return 0
}

// readHidden reads one secret line: echo off on a terminal, else the next
// line of rd.
func readHidden(prompt string, rd *bufio.Reader, errOut io.Writer) (string, error) {
	var v string
	if secretStdinIsTerminal() {
		_, _ = fmt.Fprintf(errOut, "%s (input hidden): ", prompt)
		raw, err := secretReadPassword()
		_, _ = fmt.Fprintln(errOut)
		if err != nil {
			return "", err
		}
		v = string(raw)
	} else {
		line, err := rd.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		v = line
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New("empty value; nothing saved")
	}
	return v, nil
}

// readLine prompts and reads one trimmed line ("" at end of input).
func readLine(prompt string, rd *bufio.Reader, out io.Writer) string {
	_, _ = fmt.Fprintf(out, "%s: ", prompt)
	line, _ := rd.ReadString('\n')
	_, _ = fmt.Fprintln(out)
	return strings.TrimSpace(line)
}

func splitIDs(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if f = strings.TrimSpace(f); f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

func toAnySlice(ss []string) []any {
	out := make([]any, 0, len(ss))
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}

// hubSlackStatusJSON is `itervox hub slack status --json`. It never holds a
// token: only whether each is set.
type hubSlackStatusJSON struct {
	AppToken       string            `json:"appToken"` // set | missing
	BotToken       string            `json:"botToken"`
	SettingsFile   string            `json:"settingsFile"`
	Enabled        bool              `json:"enabled"`
	AllowedUsers   []string          `json:"allowedUsers"`
	DefaultChannel string            `json:"defaultChannel,omitempty"`
	Channels       map[string]string `json:"channels"`
	HubURL         string            `json:"hubUrl,omitempty"`
	// Projects are the hub members and the channel each posts to ("" when none).
	Projects map[string]string `json:"projects"`
}

// hubSlackStatus prints what the hub's Slack channel would use: tokens set
// or missing (never their values), the user layer's hub.slack settings and
// each member project's channel.
func hubSlackStatus(asJSON bool, out, errOut io.Writer) int {
	app, bot := hubSlackTokens()
	st := hubSlackStatusJSON{AppToken: setOrMissing(app), BotToken: setOrMissing(bot),
		SettingsFile: config.UserSettingsPath(), AllowedUsers: []string{}, Channels: map[string]string{}, Projects: map[string]string{}}
	if st.SettingsFile != "" {
		raw, err := config.ReadSettingsFile(st.SettingsFile)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "itervox hub slack status: %v\n", err)
			return 1
		}
		hubSec, _ := raw["hub"].(map[string]any)
		sc, _ := hubSec["slack"].(map[string]any)
		st.Enabled, _ = sc["enabled"].(bool)
		st.AllowedUsers = append(st.AllowedUsers, anyStrings(sc["allowed_users"])...)
		st.DefaultChannel, _ = sc["default_channel"].(string)
		if chs, ok := sc["channels"].(map[string]any); ok {
			for k, v := range chs {
				if s, ok := v.(string); ok {
					st.Channels[k] = s
				}
			}
		}
	}
	if regDir, err := daemonRegistryDir(); err == nil {
		s := hubStatusIn(regDir)
		if s.Hub != nil {
			st.HubURL = s.Hub.URL
		}
		for _, m := range s.Members {
			ch := st.Channels[m.Name]
			if ch == "" {
				ch = st.DefaultChannel
			}
			st.Projects[m.Name] = ch
		}
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(st)
		return 0
	}
	_, _ = fmt.Fprintf(out, "app token (%s): %s\nbot token (%s): %s\n", slackAppTokenKey, st.AppToken, slackBotTokenKey, st.BotToken)
	_, _ = fmt.Fprintf(out, "hub.slack.enabled: %t (user settings: %s)\n", st.Enabled, st.SettingsFile)
	if len(st.AllowedUsers) == 0 {
		_, _ = fmt.Fprintln(out, "allowed users: none (alerts only; nobody may act from Slack)")
	} else {
		_, _ = fmt.Fprintf(out, "allowed users: %s\n", strings.Join(st.AllowedUsers, ", "))
	}
	if st.HubURL == "" {
		_, _ = fmt.Fprintln(out, "hub: not running")
	} else {
		_, _ = fmt.Fprintf(out, "hub: %s (its log says \"hub: slack: Socket Mode connected\" once connected)\n", st.HubURL)
	}
	names := make([]string, 0, len(st.Projects))
	for n := range st.Projects {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		ch := st.Projects[n]
		if ch == "" {
			ch = "(no channel: not posted)"
		}
		_, _ = fmt.Fprintf(out, "  %s -> %s\n", n, ch)
	}
	if !st.Enabled || st.AppToken != "set" || st.BotToken != "set" {
		_, _ = fmt.Fprintln(out, "Slack is not set up: run `itervox hub slack setup`.")
	}
	return 0
}

func setOrMissing(v string) string {
	if v == "" {
		return "missing"
	}
	return "set"
}

func anyStrings(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
