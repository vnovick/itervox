package tracker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
)

// Tracker error classes (#117). A class is what the dashboard, the API and
// `itervox doctor` show for a failed sync: a stable kind plus a summary built
// only from the error's typed fields, never from its text. Adapter errors can
// carry raw tracker payloads (Linear's GraphQL errors array, an unexpected
// response body), which must not leave the daemon log.
const (
	ErrorClassRateLimited = "rate_limited"
	ErrorClassAuth        = "auth"
	ErrorClassNotFound    = "not_found"
	ErrorClassAPIStatus   = "api_status"
	ErrorClassGraphQL     = "graphql"
	ErrorClassTimeout     = "timeout"
	ErrorClassNetwork     = "network"
	ErrorClassCanceled    = "canceled"
	ErrorClassUnknown     = "unknown"
)

// ErrorClass is a classified tracker error: Kind is one of the ErrorClass*
// constants, Summary a short fixed-vocabulary description.
type ErrorClass struct {
	Kind    string
	Summary string
}

// ClassifyError classifies err for display. The zero ErrorClass is returned
// for a nil err.
func ClassifyError(err error) ErrorClass {
	if err == nil {
		return ErrorClass{}
	}
	var rl *RateLimitedError
	if errors.As(err, &rl) {
		return ErrorClass{ErrorClassRateLimited, rl.Error()}
	}
	var st *APIStatusError
	if errors.As(err, &st) {
		if st.Status == 401 || st.Status == 403 {
			return ErrorClass{ErrorClassAuth, fmt.Sprintf("%s: api status %d (check the tracker API key)", st.Adapter, st.Status)}
		}
		return ErrorClass{ErrorClassAPIStatus, st.Error()}
	}
	var nf *NotFoundError
	if errors.As(err, &nf) {
		return ErrorClass{ErrorClassNotFound, nf.Adapter + ": resource not found"}
	}
	var gql *GraphQLError
	if errors.As(err, &gql) {
		return ErrorClass{ErrorClassGraphQL, "linear: graphql error"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorClass{ErrorClassTimeout, "tracker request timed out"}
	}
	if errors.Is(err, context.Canceled) {
		return ErrorClass{ErrorClassCanceled, "tracker request canceled"}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrorClass{ErrorClassTimeout, "tracker request timed out"}
	}
	var ue *url.Error
	var oe *net.OpError
	var de *net.DNSError
	if errors.As(err, &ue) || errors.As(err, &oe) || errors.As(err, &de) {
		return ErrorClass{ErrorClassNetwork, "tracker unreachable (network error)"}
	}
	return ErrorClass{ErrorClassUnknown, "tracker request failed (see the daemon log)"}
}
