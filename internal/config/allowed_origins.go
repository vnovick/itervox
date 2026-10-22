package config

import (
	"errors"
	"fmt"
	"slices"

	"github.com/vnovick/itervox/internal/cors"
)

// normalizeAllowedOrigins validates server.allowed_origins (#231) and
// rewrites each entry the way a browser serializes an Origin header (lower
// case, default port dropped), so the CORS check is an exact comparison.
// An entry that could never match (a path, a wildcard, no scheme) is a
// config-load error naming it, like server.allowed_hosts: a silently
// dropped origin would break exactly the hosted dashboard it was listed for.
func normalizeAllowedOrigins(entries []string) ([]string, error) {
	if entries == nil {
		return nil, nil
	}
	out := make([]string, 0, len(entries))
	var errs []error
	for _, raw := range entries {
		origin, err := cors.NormalizeOrigin(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("config: server.allowed_origins entry %q %w", raw, err))
			continue
		}
		if !slices.Contains(out, origin) {
			out = append(out, origin)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}
