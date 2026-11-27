// Copyright (c) 2026 Deepak Vankadaru

package approval

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/subcmds/cmdutil"
)

func TestPrintRequest(t *testing.T) {
	asked := time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)
	state := &gobs.ApprovalState{V1: &gobs.ApprovalStateV1{
		Config: &gobs.ApprovalConfig{
			ID: "k7q2m9", Owner: "greeler-1", Kind: "sell-to-open",
			Summary: "sell 1 NVDA 2026-12-19 160 PUT",
			Details: map[string]string{"strike": "160", "bid": "1.90"},
			AskedAt: asked, ExpiresAt: asked.Add(time.Hour),
		},
		Progress: &gobs.ApprovalProgress{Status: "rejected", Final: true, Reason: "too close", DecidedAt: asked.Add(time.Minute)},
	}}
	var buf bytes.Buffer
	printRequest(&buf, state)
	out := buf.String()
	for _, want := range []string{
		"k7q2m9  rejected (final)\n",
		"  owner:   greeler-1\n",
		"  summary: sell 1 NVDA 2026-12-19 160 PUT\n",
		"  expires: ",
		"  decided: ",
		"  reason:  too close\n",
		"  bid: 1.90\n  strike: 160\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	buf.Reset()
	printRequest(&buf, &gobs.ApprovalState{})
	if !strings.Contains(buf.String(), "incomplete") {
		t.Errorf("incomplete request printed %q", buf.String())
	}
}

func TestDecideChecksArgs(t *testing.T) {
	ctx := context.Background()
	cf := new(cmdutil.ClientFlags)
	if err := decide(ctx, cf, nil, true, false, ""); err == nil {
		t.Error("decide without an id was accepted")
	}
	if err := decide(ctx, cf, []string{"a", "b"}, true, false, ""); err == nil {
		t.Error("decide with two ids was accepted")
	}
	if err := decide(ctx, cf, []string{" "}, false, false, ""); err == nil {
		t.Error("decide with a blank id was accepted")
	}
}
