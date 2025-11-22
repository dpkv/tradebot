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
