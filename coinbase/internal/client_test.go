// Copyright (c) 2023 BVK Chaitanya

package internal

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvkgo/topic"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var (
	testingKey     string
	testingSecret  string
	testingOptions *Options = &Options{
		MaxFetchTimeLatency: time.Minute,
	}
)

func checkCredentials() bool {
	type Credentials struct {
		Key    string
		Secret string

		KID string // `json:"kid"`
		PEM string // `json:"pem"`
	}
	if len(testingKey) != 0 && len(testingSecret) != 0 {
		return true
	}
	data, err := os.ReadFile("coinbase-creds.json")
	if err != nil {
		return false
	}
	s := new(Credentials)
	if err := json.Unmarshal(data, s); err != nil {
		return false
	}
	testingKey = s.KID
	testingSecret = s.PEM
	return len(testingKey) != 0 && len(testingSecret) != 0
}

func TestClient(t *testing.T) {
	if !checkCredentials() {
		t.Skip("no credentials")
		return
	}

	topic := topic.New[*Message]()
	defer topic.Close()

	c, err := New(context.Background(), testingKey, testingSecret, testingOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	ctx := context.Background()
	if _, err := c.ListProducts(ctx, "SPOT"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := c.ListAccounts(ctx, nil); err != nil {
		t.Fatal(err)
	}

	handler := func(msg *Message) {
		if msg.Channel != "heartbeats" {
			js, _ := json.Marshal(msg)
			t.Logf("%s", js)
		}
	}

	products := []string{"DOGE-USDC"}
	ws := c.GetMessages("heartbeats", products, handler)
	ws.Subscribe("user", products)
	ws.Subscribe("ticker", products)

	time.Sleep(30 * time.Second)

	createReq := &CreateOrderRequest{
		ClientOrderID: uuid.New().String(),
		ProductID:     "DOGE-USDC",
		Side:          "BUY",
		Order: &OrderConfig{
			LimitGTC: &LimitLimitGTC{
				BaseSize:   exchange.NullDecimal{Decimal: decimal.NewFromInt(1000)},
				LimitPrice: exchange.NullDecimal{Decimal: decimal.NewFromFloat(0.20)},
				PostOnly:   true,
			},
		},
	}
	createResp, err := c.CreateOrder(ctx, createReq)
	if err != nil {
		t.Fatal(err)
	} else {
		js, _ := json.MarshalIndent(createResp, "", "  ")
		t.Logf("%s", js)
	}
	defer func() {
		cancelReq := &CancelOrderRequest{
			OrderIDs: []string{createResp.SuccessResponse.OrderID},
		}
		if _, err := c.CancelOrder(ctx, cancelReq); err != nil {
			t.Fatal(err)
		}
	}()

	time.Sleep(30 * time.Second)
}

func TestEditOrder(t *testing.T) {
	if !checkCredentials() {
		t.Skip("no credentials")
		return
	}

	topic := topic.New[*Message]()
	defer topic.Close()

	c, err := New(context.Background(), testingKey, testingSecret, testingOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	ctx := context.Background()
	prodsResp, err := c.ListProducts(ctx, "SPOT")
	if err != nil {
		t.Fatal(err)
	}
	// Find the current price for BTC-USD.
	var btcPrice decimal.Decimal
	for _, p := range prodsResp.Products {
		if p.ProductID == "BTC-USD" {
			btcPrice = p.Price.Decimal
			break
		}
	}
	if btcPrice.IsZero() {
		t.Fatalf("could not determine current BTC-USD price")
	}
	t.Logf("current BTC-USD price is %s", btcPrice)

	// Pick a price-point for the order that is 20% below the current price.
	product, size, price := "BTC-USD", 0.001, btcPrice.Mul(decimal.NewFromFloat(0.80)).Round(2)

	createReq := &CreateOrderRequest{
		ClientOrderID: uuid.New().String(),
		ProductID:     product,
		Side:          "BUY",
		Order: &OrderConfig{
			LimitGTC: &LimitLimitGTC{
				BaseSize:   exchange.NullDecimal{Decimal: decimal.NewFromFloat(size)},
				LimitPrice: exchange.NullDecimal{Decimal: price},
				PostOnly:   true,
			},
		},
	}
	createResp, err := c.CreateOrder(ctx, createReq)
	if err != nil {
		t.Fatal(err)
	} else {
		js, _ := json.MarshalIndent(createResp, "", "  ")
		t.Logf("%s", js)
	}
	if !createResp.Success {
		t.Fatalf("create order is not successful")
	}
	defer func() {
		cancelReq := &CancelOrderRequest{
			OrderIDs: []string{createResp.SuccessResponse.OrderID},
		}
		cancelResp, err := c.CancelOrder(ctx, cancelReq)
		if err != nil {
			t.Fatal(err)
		} else {
			js, _ := json.MarshalIndent(cancelResp, "", "  ")
			t.Logf("%s", js)
		}
		for _, r := range cancelResp.Results {
			t.Logf("cancel order %s success: %v", r.OrderID, r.Success)
		}
	}()

	editReq := &EditOrderRequest{
		OrderID: createResp.SuccessResponse.OrderID,
		Price:   exchange.NullDecimal{Decimal: price},
		Size:    exchange.NullDecimal{Decimal: decimal.NewFromFloat(size * 2)},
	}
	editResp, err := c.EditOrder(ctx, editReq)
	if err != nil {
		t.Fatal(err)
	} else {
		js, _ := json.MarshalIndent(editResp, "", "  ")
		t.Logf("%s", js)
	}
	if !editResp.Success {
		t.Fatalf("edit order is not successful")
	}
	t.Logf("edit order is successful")
}

func TestEditStopLimitOrder(t *testing.T) {
	if !checkCredentials() {
		t.Skip("no credentials")
		return
	}

	topic := topic.New[*Message]()
	defer topic.Close()

	c, err := New(context.Background(), testingKey, testingSecret, testingOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	ctx := context.Background()
	prodsResp, err := c.ListProducts(ctx, "SPOT")
	if err != nil {
		t.Fatal(err)
	}
	// Find the current price for BTC-USD.
	var btcPrice decimal.Decimal
	for _, p := range prodsResp.Products {
		if p.ProductID == "BTC-USD" {
			btcPrice = p.Price.Decimal
			break
		}
	}
	if btcPrice.IsZero() {
		t.Fatalf("could not determine current BTC-USD price")
	}
	t.Logf("current BTC-USD price is %s", btcPrice)

	// For a stop limit BUY order, set stop price 20% above current price.
	// Limit price slightly above stop price.
	product := "BTC-USD"
	size := 0.00001
	stopPrice := btcPrice.Mul(decimal.NewFromFloat(1.20)).Round(2)
	limitPrice := stopPrice.Mul(decimal.NewFromFloat(1.01)).Round(2)

	createReq := &CreateOrderRequest{
		ClientOrderID: uuid.New().String(),
		ProductID:     product,
		Side:          "BUY",
		Order: &OrderConfig{
			StopLimitGTC: &StopLimitStopLimitGTC{
				BaseSize:      exchange.NullDecimal{Decimal: decimal.NewFromFloat(size)},
				StopPrice:     exchange.NullDecimal{Decimal: stopPrice},
				LimitPrice:    exchange.NullDecimal{Decimal: limitPrice},
				StopDirection: "STOP_DIRECTION_STOP_UP",
			},
		},
	}
	createResp, err := c.CreateOrder(ctx, createReq)
	if err != nil {
		t.Fatal(err)
	} else {
		js, _ := json.MarshalIndent(createResp, "", "  ")
		t.Logf("%s", js)
	}
	if !createResp.Success {
		t.Fatalf("create order is not successful")
	}
	defer func() {
		cancelReq := &CancelOrderRequest{
			OrderIDs: []string{createResp.SuccessResponse.OrderID},
		}
		cancelResp, err := c.CancelOrder(ctx, cancelReq)
		if err != nil {
			t.Fatal(err)
		} else {
			js, _ := json.MarshalIndent(cancelResp, "", "  ")
			t.Logf("%s", js)
		}
		for _, r := range cancelResp.Results {
			t.Logf("cancel order %s success: %v", r.OrderID, r.Success)
		}
	}()

	// Edit the order: double the size and adjust stop price.
	newStopPrice := btcPrice.Mul(decimal.NewFromFloat(1.25)).Round(2)
	newLimitPrice := newStopPrice.Mul(decimal.NewFromFloat(1.01)).Round(2)
	editReq := &EditOrderRequest{
		OrderID:   createResp.SuccessResponse.OrderID,
		Price:     exchange.NullDecimal{Decimal: newLimitPrice},
		Size:      exchange.NullDecimal{Decimal: decimal.NewFromFloat(size * 2)},
		StopPrice: exchange.NullDecimal{Decimal: newStopPrice},
	}
	editResp, err := c.EditOrder(ctx, editReq)
	if err != nil {
		t.Fatal(err)
	} else {
		js, _ := json.MarshalIndent(editResp, "", "  ")
		t.Logf("%s", js)
	}
	if !editResp.Success {
		t.Fatalf("edit order is not successful")
	}
	t.Logf("edit stop limit order is successful")
}
