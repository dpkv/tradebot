// Copyright (c) 2025 Deepak Vankadaru

package alpaca

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

var (
	testingKey    string
	testingSecret string
	testingPaper  bool
)

func checkCredentials() bool {
	if len(testingKey) != 0 && len(testingSecret) != 0 {
		return true
	}
	data, err := os.ReadFile("alpaca-creds.json")
	if err != nil {
		return false
	}
	s := new(Credentials)
	if err := json.Unmarshal(data, s); err != nil {
		return false
	}
	testingKey = s.Key
	testingSecret = s.Secret
	testingPaper = s.PaperTrading
	return len(testingKey) != 0 && len(testingSecret) != 0
}

func TestClientGetAsset(t *testing.T) {
	if !checkCredentials() {
		t.Skip("no credentials")
		return
	}

	ctx := context.Background()

	opts := &Options{}
	opts.setDefaults(testingPaper)
	c, err := NewClient(ctx, testingKey, testingSecret, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Test GetAsset with a common symbol (AAPL)
	asset, err := c.GetAsset(ctx, "AAPL")
	if err != nil {
		t.Fatal(err)
	}

	if asset == nil {
		t.Fatal("asset is nil")
	}

	t.Logf("Asset Symbol: %s", asset.Symbol)
	t.Logf("Asset Name: %s", asset.Name)
	t.Logf("Asset Class: %s", asset.Class)
	t.Logf("Asset Exchange: %s", asset.Exchange)
	t.Logf("Asset Status: %s", asset.Status)
	t.Logf("Asset Tradable: %v", asset.Tradable)
	t.Logf("Asset Marginable: %v", asset.Marginable)
	t.Logf("Asset Shortable: %v", asset.Shortable)
	t.Logf("Asset EasyToBorrow: %v", asset.EasyToBorrow)

	jsdata, _ := json.MarshalIndent(asset, "", "  ")
	t.Logf("%s", jsdata)
}

func TestClientGetAssets(t *testing.T) {
	if !checkCredentials() {
		t.Skip("no credentials")
		return
	}

	ctx := context.Background()

	opts := &Options{}
	opts.setDefaults(testingPaper)
	c, err := NewClient(ctx, testingKey, testingSecret, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Test GetAssets without filters (get all assets)
	assets, err := c.GetAssets(ctx, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if assets == nil {
		t.Fatal("assets is nil")
	}

	if len(assets) == 0 {
		t.Log("Warning: no assets returned")
	} else {
		t.Logf("Retrieved %d assets", len(assets))
		// Log first few assets
		for i, asset := range assets {
			if i >= 5 {
				break
			}
			t.Logf("Asset %d: Symbol=%s, Name=%s, Status=%s, Class=%s, Exchange=%s, Tradable=%v", i+1, asset.Symbol, asset.Name, asset.Status, asset.Class, asset.Exchange, asset.Tradable)
		}
	}

	// Test GetAssets with status filter (active assets only)
	status := "active"
	activeAssets, err := c.GetAssets(ctx, &status, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if activeAssets == nil {
		t.Fatal("activeAssets is nil")
	}

	t.Logf("Retrieved %d active assets", len(activeAssets))
	if len(activeAssets) > 0 {
		jsdata, _ := json.MarshalIndent(activeAssets[0], "", "  ")
		t.Logf("First active asset: %s", jsdata)
	}

	// Test GetAssets with asset class filter (us_equity)
	assetClass := "us_equity"
	equityAssets, err := c.GetAssets(ctx, nil, &assetClass, nil)
	if err != nil {
		t.Fatal(err)
	}

	if equityAssets == nil {
		t.Fatal("equityAssets is nil")
	}

	t.Logf("Retrieved %d equity assets", len(equityAssets))
	if len(equityAssets) > 0 {
		t.Logf("First equity asset: Symbol=%s, Class=%s", equityAssets[0].Symbol, equityAssets[0].Class)
	}

	// Test GetAssets with exchange filter (NYSE)
	exchange := "NYSE"
	nyseAssets, err := c.GetAssets(ctx, nil, nil, &exchange)
	if err != nil {
		t.Fatal(err)
	}

	if nyseAssets == nil {
		t.Fatal("nyseAssets is nil")
	}

	t.Logf("Retrieved %d NYSE assets", len(nyseAssets))
	if len(nyseAssets) > 0 {
		t.Logf("First NYSE asset: Symbol=%s, Exchange=%s", nyseAssets[0].Symbol, nyseAssets[0].Exchange)
	}

	// Test GetAssets with multiple filters (active, equity, NYSE)
	activeEquityNyse, err := c.GetAssets(ctx, &status, &assetClass, &exchange)
	if err != nil {
		t.Fatal(err)
	}

	if activeEquityNyse == nil {
		t.Fatal("activeEquityNyse is nil")
	}

	t.Logf("Retrieved %d active equity assets on NYSE", len(activeEquityNyse))
	if len(activeEquityNyse) > 0 {
		jsdata, _ := json.MarshalIndent(activeEquityNyse[0], "", "  ")
		t.Logf("First active equity NYSE asset: %s", jsdata)
	}
}

func TestClientGetSnapshot(t *testing.T) {
	if !checkCredentials() {
		t.Skip("no credentials")
		return
	}

	ctx := context.Background()

	opts := &Options{}
	opts.setDefaults(testingPaper)
	c, err := NewClient(ctx, testingKey, testingSecret, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Test GetSnapshot with a common symbol (AAPL)
	snapshot, err := c.GetSnapshot(ctx, "AAPL")
	if err != nil {
		t.Fatal(err)
	}

	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}

	// Log latest trade information
	if snapshot.LatestTrade != nil {
		t.Logf("Latest Trade - Price: %.2f, Size: %d, Timestamp: %v",
			snapshot.LatestTrade.Price,
			snapshot.LatestTrade.Size,
			snapshot.LatestTrade.Timestamp)
	} else {
		t.Log("Latest Trade: nil")
	}

	// Log latest quote information
	if snapshot.LatestQuote != nil {
		t.Logf("Latest Quote - Bid: %.2f (Size: %d), Ask: %.2f (Size: %d), Timestamp: %v",
			snapshot.LatestQuote.BidPrice,
			snapshot.LatestQuote.BidSize,
			snapshot.LatestQuote.AskPrice,
			snapshot.LatestQuote.AskSize,
			snapshot.LatestQuote.Timestamp)
	} else {
		t.Log("Latest Quote: nil")
	}

	// Log daily bar information
	if snapshot.DailyBar != nil {
		t.Logf("Daily Bar - Open: %.2f, High: %.2f, Low: %.2f, Close: %.2f, Volume: %d, Timestamp: %v",
			snapshot.DailyBar.Open,
			snapshot.DailyBar.High,
			snapshot.DailyBar.Low,
			snapshot.DailyBar.Close,
			snapshot.DailyBar.Volume,
			snapshot.DailyBar.Timestamp)
	} else {
		t.Log("Daily Bar: nil")
	}

	// Log minute bar information
	if snapshot.MinuteBar != nil {
		t.Logf("Minute Bar - Open: %.2f, High: %.2f, Low: %.2f, Close: %.2f, Volume: %d, Timestamp: %v",
			snapshot.MinuteBar.Open,
			snapshot.MinuteBar.High,
			snapshot.MinuteBar.Low,
			snapshot.MinuteBar.Close,
			snapshot.MinuteBar.Volume,
			snapshot.MinuteBar.Timestamp)
	} else {
		t.Log("Minute Bar: nil")
	}

	// Log previous daily bar information
	if snapshot.PrevDailyBar != nil {
		t.Logf("Previous Daily Bar - Open: %.2f, High: %.2f, Low: %.2f, Close: %.2f, Volume: %d, Timestamp: %v",
			snapshot.PrevDailyBar.Open,
			snapshot.PrevDailyBar.High,
			snapshot.PrevDailyBar.Low,
			snapshot.PrevDailyBar.Close,
			snapshot.PrevDailyBar.Volume,
			snapshot.PrevDailyBar.Timestamp)
	} else {
		t.Log("Previous Daily Bar: nil")
	}

	jsdata, _ := json.MarshalIndent(snapshot, "", "  ")
	t.Logf("Full snapshot: %s", jsdata)
}

func TestClientGetAccount(t *testing.T) {
	if !checkCredentials() {
		t.Skip("no credentials")
		return
	}

	ctx := context.Background()

	opts := &Options{}
	opts.setDefaults(testingPaper)
	c, err := NewClient(ctx, testingKey, testingSecret, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Test GetAccount
	account, err := c.GetAccount(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if account == nil {
		t.Fatal("account is nil")
	}

	t.Logf("Account ID: %s", account.ID)
	t.Logf("Account Number: %s", account.AccountNumber)
	t.Logf("Status: %s", account.Status)
	t.Logf("Currency: %s", account.Currency)
	t.Logf("Cash: %s", account.Cash.String())
	t.Logf("Buying Power: %s", account.BuyingPower.String())
	t.Logf("Portfolio Value: %s", account.PortfolioValue.String())
	t.Logf("Equity: %s", account.Equity.String())
	t.Logf("Pattern Day Trader: %v", account.PatternDayTrader)
	t.Logf("Trading Blocked: %v", account.TradingBlocked)
	t.Logf("Account Blocked: %v", account.AccountBlocked)
	t.Logf("Shorting Enabled: %v", account.ShortingEnabled)
	t.Logf("Created At: %v", account.CreatedAt)

	jsdata, _ := json.MarshalIndent(account, "", "  ")
	t.Logf("Full account: %s", jsdata)
}
