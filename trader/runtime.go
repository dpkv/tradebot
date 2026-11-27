// Copyright (c) 2023 BVK Chaitanya

package trader

import (
	"context"
	"time"

	"github.com/bvk/tradebot/approval"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvkgo/kv"
)

type Messenger interface {
	SendMessage(context.Context, time.Time, string, ...interface{})
}

type Runtime struct {
	Account   exchange.Account
	Database  kv.Database
	Product   exchange.Product
	Messenger Messenger

	// Approver keeps operator sign-off requests (greel/approval-story.md);
	// nil when the runtime has none.
	Approver approval.Approver
}
