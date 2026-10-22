package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jsonKeys lists the JSON names of t's fields.
func jsonKeys(t reflect.Type) []string {
	var out []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestPublishedNotificationsSchemaMatchesParser (#229): the JSON Schema at
// NotificationsSchemaURL (site/public/schema/notifications.schema.json)
// describes exactly what ParseNotificationSettings accepts: the same
// top-level and project fields, kinds, channels, version and digest
// interval bounds.
func TestPublishedNotificationsSchemaMatchesParser(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "site", "public", "schema", "notifications.schema.json"))
	require.NoError(t, err)
	type prop struct {
		Const   string `json:"const"`
		Enum    []any  `json:"enum"`
		Minimum int    `json:"minimum"`
		Maximum int    `json:"maximum"`
		Default int    `json:"default"`
	}
	var schema struct {
		ID         string          `json:"$id"`
		Properties map[string]prop `json:"properties"`
		Defs       struct {
			ChannelName struct {
				Enum []string `json:"enum"`
			} `json:"channelName"`
			Routes struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"routes"`
			Project struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"project"`
			Channel struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"channel"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	var top struct {
		Properties struct {
			Channels struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"channels"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &top))

	assert.Equal(t, NotificationsSchemaURL, schema.ID)
	assert.Equal(t, NotificationsSchemaURL, schema.Properties["$schema"].Const)
	assert.Equal(t, jsonKeys(reflect.TypeFor[NotificationSettings]()), sortedKeys(schema.Properties))
	assert.Equal(t, jsonKeys(reflect.TypeFor[ProjectNotificationSettings]()), sortedKeys(schema.Defs.Project.Properties))
	assert.Equal(t, jsonKeys(reflect.TypeFor[ChannelSettings]()), sortedKeys(schema.Defs.Channel.Properties))
	assert.ElementsMatch(t, NotificationKinds, sortedKeys(schema.Defs.Routes.Properties))
	assert.Equal(t, ChannelNames, schema.Defs.ChannelName.Enum)
	assert.ElementsMatch(t, ChannelNames, sortedKeys(top.Properties.Channels.Properties))
	assert.Equal(t, []any{float64(notificationsVersion)}, schema.Properties["version"].Enum)
	di := schema.Properties["digest_interval_minutes"]
	assert.Equal(t, 1, di.Minimum)
	assert.Equal(t, maxDigestIntervalMinutes, di.Maximum)
	assert.Equal(t, DefaultDigestIntervalMinutes, di.Default)

	// The defaults round-trip through the parser.
	data, err := json.Marshal(DefaultNotificationSettings())
	require.NoError(t, err)
	parsed, err := ParseNotificationSettings(data)
	require.NoError(t, err)
	assert.Equal(t, DefaultNotificationSettings(), parsed)
	_, err = ParseNotificationSettings([]byte(`{"quiet_hours": true}`))
	assert.Error(t, err, "an unknown field is refused")
	_, err = ParseNotificationSettings([]byte(`{"digest_interval_minutes": 1441}`))
	assert.Error(t, err)
}
