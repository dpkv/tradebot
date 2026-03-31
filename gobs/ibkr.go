// Copyright (c) 2026 Deepak Vankadaru

package gobs

import "encoding/json"

// IBKROrder holds a persisted IBKR order, keyed by client order ID.
type IBKROrder struct {
	ClientOrderID string
	Order         json.RawMessage
}
