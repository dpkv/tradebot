// Copyright (c) 2026 Deepak Vankadaru

package greelladder

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/trader"
	"github.com/bvkgo/kv"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

func alerts(v *GreelLadder) []string {
	var msgs []string
	for {
		select {
		case msg := <-v.alertCh:
			msgs = append(msgs, msg)
		default:
			return msgs
		}
	}
}

func TestAdmitMaxContracts(t *testing.T) {
	v := newTestLadder(t, band(100))
	clock := &testClock{now: time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)}
	v.now = clock.Now
	a := &fakeSibling{uid: "a", known: true, optType: "CALL"}
	b := &fakeSibling{uid: "b", known: true}
	c := &fakeSibling{uid: "c", known: true}
	v.siblings = []sibling{a, b, c}

	// No limit: everything goes.
	if !v.admit("b", "PUT", d("10000")) {
		t.Fatalf("admitted nothing without limits")
	}
	delete(v.reservations, "b")

	if _, err := v.SetOption("max-contracts", "2"); err != nil {
		t.Fatal(err)
	}
	// a writes a call; b's flip makes two.
	if !v.admit("b", "PUT", d("10000")) {
		t.Fatalf("b held at 2 of 2 contracts")
	}
	// b is reserved but not in wheel mode yet: c would make three.
	if v.admit("c", "CALL", decimal.Zero) {
		t.Fatalf("c admitted over the limit while b's flip is in flight")
	}
	if v.admit("c", "CALL", decimal.Zero) {
		t.Fatalf("c admitted on its second try")
	}
	msgs := alerts(v)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "3 contracts, over its limit of 2") {
		t.Fatalf("alerts = %q, want one", msgs)
	}

	// b reports wheel mode; its reservation no longer counts twice.
	b.optType, b.exposure = "PUT", d("10000")
	if v.admit("c", "CALL", decimal.Zero) {
		t.Fatalf("c admitted with a and b writing")
	}
	// a's call settles: room for c, and a later block alerts again.
	a.optType = ""
	if !v.admit("c", "CALL", decimal.Zero) {
		t.Fatalf("c held after a's call settled")
	}
	if msgs := alerts(v); len(msgs) != 0 {
		t.Fatalf("alerts after admission = %q", msgs)
	}

	// A reservation lapses: a flip that never happened frees its room.
	a.optType, b.optType = "", ""
	clock.Add(claimTTL)
	if !v.admit("a", "PUT", d("1")) || !v.admit("b", "PUT", d("1")) {
		t.Fatalf("lapsed reservations still count")
	}
}

func TestAdmitMaxPutExposure(t *testing.T) {
	v := newTestLadder(t, band(100))
	v.now = (&testClock{now: time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)}).Now
	a := &fakeSibling{uid: "a", known: true, optType: "PUT", exposure: d("10000")}
	b := &fakeSibling{uid: "b", known: true, optType: "CALL"}
	c := &fakeSibling{uid: "c", known: true}
	v.siblings = []sibling{a, b, c}
	if _, err := v.SetOption("max-put-exposure", "20000"); err != nil {
		t.Fatal(err)
	}
	if v.admit("c", "PUT", d("10000.01")) {
		t.Fatalf("c admitted over the exposure limit")
	}
	if msgs := alerts(v); len(msgs) != 1 || !strings.Contains(msgs[0], "put exposure would be 20000.01, over its limit of 20000.00") {
		t.Fatalf("alerts = %q", msgs)
	}
	// Calls carry no exposure.
	if !v.admit("c", "CALL", decimal.Zero) {
		t.Fatalf("a call was held by the put exposure limit")
	}
	delete(v.reservations, "c")
	if !v.admit("c", "PUT", d("10000")) {
		t.Fatalf("c held at exactly the limit")
	}
}

func TestLimitOptions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	v := newTestLadder(t, band(100), band(110))
	for _, bad := range [][2]string{{"max-contracts", "-1"}, {"max-contracts", "x"}, {"max-put-exposure", "-5"}, {"max-put-exposure", ""}} {
		if _, err := v.SetOption(bad[0], bad[1]); err == nil {
			t.Errorf("SetOption(%s, %q) succeeded", bad[0], bad[1])
		}
	}
	undo, err := v.SetOption("MAX-CONTRACTS", "3")
	if err != nil || undo != "0" {
		t.Fatalf("max-contracts = %q, %v", undo, err)
	}
	if undo, err := v.SetOption("max-put-exposure", "25000.50"); err != nil || undo != "0" {
		t.Fatalf("max-put-exposure = %q, %v", undo, err)
	}
	// The ladder keeps its limits; the greelers don't get them.
	for _, g := range v.Greelers() {
		if _, err := g.SetOption("max-contracts", "3"); err == nil {
			t.Fatalf("greelers accept ladder limits")
		}
	}
	if err := kv.WithReadWriter(ctx, db, v.Save); err != nil {
		t.Fatal(err)
	}
	gv, err := kvutil.GetDB[gobs.GreelLadderState](ctx, db, DefaultKeyspace+v.UID())
	if err != nil {
		t.Fatal(err)
	}
	if o := gv.V1.Options; o["max-contracts"] != "3" || o["max-put-exposure"] != "25000.5" {
		t.Fatalf("saved options = %v", o)
	}
	var w *GreelLadder
	if err := kv.WithReader(ctx, db, func(ctx context.Context, r kv.Reader) (err error) {
		w, err = Load(ctx, v.UID(), r)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w.maxContracts != 3 || !w.maxPutExposure.Equal(d("25000.5")) {
		t.Fatalf("loaded limits = %d, %s", w.maxContracts, w.maxPutExposure)
	}
	// Undo restores no limit, which saves as no options.
	if _, err := w.SetOption("max-contracts", undo); err != nil {
		t.Fatal(err)
	}
	if _, err := w.SetOption("max-put-exposure", "0"); err != nil {
		t.Fatal(err)
	}
	if o := w.options(); o != nil {
		t.Errorf("options without limits = %v", o)
	}
}

// TestLoadedGreelersAreGated: a ladder's own greelers answer the gates
// from their saved records, before any Run.
func TestLoadedGreelersAreGated(t *testing.T) {
	v := newTestLadder(t, band(100), band(110))
	if _, err := v.SetOption("max-contracts", "1"); err != nil {
		t.Fatal(err)
	}
	g0, g1 := v.cfg.GreelerIDs[0], v.cfg.GreelerIDs[1]
	if !v.admit(g0, "PUT", d("10000")) {
		t.Fatalf("first greeler held with nothing written")
	}
	if v.admit(g1, "PUT", d("11000")) {
		t.Fatalf("second greeler admitted while the first one's flip is reserved")
	}
}

// TestRunSendsGateAlerts: a block found while the greelers run reaches the
// messenger through Run.
func TestRunSendsGateAlerts(t *testing.T) {
	db := newTestDB(t)
	stock := &fakeStock{prices: topic.New[exchange.PriceUpdate]()}
	msgr := &fakeMessenger{}
	rt := &trader.Runtime{Exchange: &fakeExchange{}, Database: db, Product: stock, Messenger: msgr}
	v := newTestLadder(t, band(98), band(102))
	if _, err := v.SetOption("max-contracts", "1"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- v.Run(ctx, rt) }()

	g0, g1 := v.cfg.GreelerIDs[0], v.cfg.GreelerIDs[1]
	if !v.admit(g0, "PUT", d("9800")) || v.admit(g1, "PUT", d("10200")) {
		t.Fatalf("max-contracts=1 admitted both or neither")
	}
	waitFor(t, "a gate alert", func() bool { return len(msgr.messages()) > 0 })
	if msg := msgr.messages()[0]; !strings.Contains(msg, "holds greeler "+g1) {
		t.Errorf("alert = %q", msg)
	}
	cancel()
	<-errCh
}
