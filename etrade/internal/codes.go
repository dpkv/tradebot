// Copyright (c) 2026 Deepak Vankadaru

package internal

// HTTP status codes returned by the E*TRADE API that require special
// handling in the client. These duplicate net/http constants intentionally —
// naming them here makes the client code explicit about which E*TRADE
// behaviors each status code triggers, rather than scattering raw numbers.
const (
	// StatusUnauthorized is returned when the OAuth access token is expired or
	// invalid. The client treats this as a signal to attempt token renewal.
	StatusUnauthorized = 401

	// StatusNotFound is returned when the requested resource (e.g. an order)
	// does not exist on the exchange. The client maps this to os.ErrNotExist.
	StatusNotFound = 404

	// StatusTooManyRequests is returned when the API rate limit is exceeded.
	// The client backs off and retries after a delay.
	StatusTooManyRequests = 429
)
