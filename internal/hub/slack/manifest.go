package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// The Slack app manifest `itervox hub slack setup` prints: paste it at
// api.slack.com/apps → Create New App → From a manifest. Socket Mode on (no
// request URL, no inbound port), interactivity on, the /itervox command,
// and the bot scopes chat:write, commands and im:write.

// BotScopes are the bot token scopes the app asks for.
var BotScopes = []string{"chat:write", "commands", "im:write"}

// SlashCommand is the steering command.
const SlashCommand = "/itervox"

// AppManifest is a Slack app manifest (the fields Itervox sets).
type AppManifest struct {
	DisplayInformation ManifestDisplay  `json:"display_information" yaml:"display_information"`
	Features           ManifestFeatures `json:"features" yaml:"features"`
	OAuthConfig        ManifestOAuth    `json:"oauth_config" yaml:"oauth_config"`
	Settings           ManifestSettings `json:"settings" yaml:"settings"`
}

// ManifestDisplay is the app's name and description.
type ManifestDisplay struct {
	Name            string `json:"name" yaml:"name"`
	Description     string `json:"description" yaml:"description"`
	LongDescription string `json:"long_description,omitempty" yaml:"long_description,omitempty"`
	BackgroundColor string `json:"background_color" yaml:"background_color"`
}

// ManifestFeatures are the bot user and slash commands.
type ManifestFeatures struct {
	BotUser       ManifestBotUser   `json:"bot_user" yaml:"bot_user"`
	SlashCommands []ManifestCommand `json:"slash_commands" yaml:"slash_commands"`
}

// ManifestBotUser is the app's bot user.
type ManifestBotUser struct {
	DisplayName  string `json:"display_name" yaml:"display_name"`
	AlwaysOnline bool   `json:"always_online" yaml:"always_online"`
}

// ManifestCommand is one slash command (no URL: Socket Mode).
type ManifestCommand struct {
	Command      string `json:"command" yaml:"command"`
	Description  string `json:"description" yaml:"description"`
	UsageHint    string `json:"usage_hint" yaml:"usage_hint"`
	ShouldEscape bool   `json:"should_escape" yaml:"should_escape"`
}

// ManifestOAuth holds the scopes.
type ManifestOAuth struct {
	Scopes ManifestScopes `json:"scopes" yaml:"scopes"`
}

// ManifestScopes are the bot scopes.
type ManifestScopes struct {
	Bot []string `json:"bot" yaml:"bot"`
}

// ManifestSettings turns Socket Mode and interactivity on.
type ManifestSettings struct {
	Interactivity        ManifestInteractivity `json:"interactivity" yaml:"interactivity"`
	OrgDeployEnabled     bool                  `json:"org_deploy_enabled" yaml:"org_deploy_enabled"`
	SocketModeEnabled    bool                  `json:"socket_mode_enabled" yaml:"socket_mode_enabled"`
	TokenRotationEnabled bool                  `json:"token_rotation_enabled" yaml:"token_rotation_enabled"`
}

// ManifestInteractivity turns interactive components on.
type ManifestInteractivity struct {
	IsEnabled bool `json:"is_enabled" yaml:"is_enabled"`
}

// Manifest is the app manifest Itervox needs.
func Manifest() AppManifest {
	return AppManifest{
		DisplayInformation: ManifestDisplay{
			Name:        "Itervox",
			Description: "Itervox hub: agent questions, approvals and steering",
			LongDescription: "Your own Itervox hub's Slack app. It posts when an agent needs input, a guardrail " +
				"needs approval, a run fails or a review lands, takes your reply or decision back to the " +
				"project, and steers a running agent with /itervox steer. It connects over Socket Mode from " +
				"the machine that runs the hub, so no inbound port or public URL is needed.",
			BackgroundColor: "#1f2937",
		},
		Features: ManifestFeatures{
			BotUser: ManifestBotUser{DisplayName: "Itervox"},
			SlashCommands: []ManifestCommand{{
				Command: SlashCommand, Description: "Steer a running Itervox agent",
				UsageHint: "steer [project/]ISSUE text",
			}},
		},
		OAuthConfig: ManifestOAuth{Scopes: ManifestScopes{Bot: slices.Clone(BotScopes)}},
		Settings: ManifestSettings{
			Interactivity:     ManifestInteractivity{IsEnabled: true},
			SocketModeEnabled: true,
		},
	}
}

// ManifestYAML renders the manifest as YAML.
func ManifestYAML() string {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	_ = enc.Encode(Manifest())
	_ = enc.Close()
	return b.String()
}

// ManifestJSON renders the manifest as indented JSON.
func ManifestJSON() string {
	data, _ := json.MarshalIndent(Manifest(), "", "  ")
	return string(data) + "\n"
}

var (
	commandRe = regexp.MustCompile(`^/[a-z0-9_-]{1,31}$`)
	colorRe   = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// ValidateManifest checks m against Slack's manifest rules for the fields
// Itervox sets (lengths, formats, and the Socket Mode setup this channel
// needs: Socket Mode and interactivity on, no request URLs, the three bot
// scopes and the /itervox command).
func ValidateManifest(m AppManifest) error {
	d := m.DisplayInformation
	var errs []error
	check := func(ok bool, format string, a ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf("slack manifest: "+format, a...))
		}
	}
	check(d.Name != "" && len(d.Name) <= 35, "display_information.name must be 1-35 characters")
	check(len(d.Description) <= 140, "display_information.description is longer than 140 characters")
	check(len(d.LongDescription) == 0 || (len(d.LongDescription) >= 174 && len(d.LongDescription) <= 4000),
		"display_information.long_description must be 174-4000 characters")
	check(d.BackgroundColor == "" || colorRe.MatchString(d.BackgroundColor), "background_color must be #RRGGBB")
	bu := m.Features.BotUser
	check(bu.DisplayName != "" && len(bu.DisplayName) <= 80, "features.bot_user.display_name must be 1-80 characters")
	check(len(m.Features.SlashCommands) == 1, "exactly one slash command")
	for _, sc := range m.Features.SlashCommands {
		check(commandRe.MatchString(sc.Command), "slash command %q must be / plus up to 31 lowercase characters", sc.Command)
		check(sc.Command == SlashCommand, "the slash command must be %s", SlashCommand)
		check(sc.Description != "" && len(sc.Description) <= 2000, "slash command description must be 1-2000 characters")
		check(len(sc.UsageHint) <= 1000, "slash command usage_hint is too long")
	}
	scopes := slices.Clone(m.OAuthConfig.Scopes.Bot)
	slices.Sort(scopes)
	want := slices.Clone(BotScopes)
	slices.Sort(want)
	check(slices.Equal(scopes, want), "bot scopes must be exactly %s", strings.Join(want, ", "))
	check(m.Settings.SocketModeEnabled, "settings.socket_mode_enabled must be true")
	check(m.Settings.Interactivity.IsEnabled, "settings.interactivity.is_enabled must be true")
	return errors.Join(errs...)
}
