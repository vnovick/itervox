package linear

import (
	"context"
	"fmt"
)

// ProbeViewer is a read-only credential check (CORE-063, `itervox doctor
// --deploy`): one `{ viewer { id name } }` query through the client's normal
// GraphQL transport (same endpoint, auth header and rate-limit handling as
// every other call). It returns the authenticated user's name (or id).
func (c *Client) ProbeViewer(ctx context.Context) (string, error) {
	data, err := c.graphql(ctx, `query ItervoxDoctorProbe { viewer { id name } }`, nil)
	if err != nil {
		return "", err
	}
	payload, _ := data["data"].(map[string]any)
	viewer, _ := payload["viewer"].(map[string]any)
	if viewer == nil {
		return "", decodeError(data)
	}
	if name, _ := viewer["name"].(string); name != "" {
		return name, nil
	}
	if id, _ := viewer["id"].(string); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("linear_probe: viewer has no id")
}
