// Copyright (c) 2026 Deepak Vankadaru

package greel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/greeler"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/namer"
	"github.com/bvk/tradebot/trader"
	"github.com/bvkgo/kv"
)

// resolveUID takes a job name or a uid.
func resolveUID(ctx context.Context, r kv.Reader, arg string) (string, error) {
	_, uid, _, err := namer.Resolve(ctx, r, arg)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("could not resolve job argument %q: %w", arg, err)
		}
		return arg, nil
	}
	return uid, nil
}

// listKeys prints the keys under keyspace that keep accepts.
func listKeys(ctx context.Context, r kv.Reader, keyspace string, keep func(string) bool) error {
	it, err := r.Ascend(ctx, keyspace, "")
	if err != nil {
		return err
	}
	defer kv.Close(it)

	for k, _, err := it.Fetch(ctx, false); err == nil; k, _, err = it.Fetch(ctx, true) {
		if !strings.HasPrefix(k, keyspace) {
			break
		}
		if keep(k) {
			fmt.Println(k)
		}
	}
	if _, _, err := it.Fetch(ctx, false); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// printJSON prints the record at key as indented JSON.
func printJSON[T any](ctx context.Context, r kv.Reader, key string) error {
	gv, err := kvutil.Get[T](ctx, r, key)
	if err != nil {
		return fmt.Errorf("could not load state from key %q: %w", key, err)
	}
	d, _ := json.MarshalIndent(gv, "", "  ")
	fmt.Printf("%s\n", d)
	return nil
}

func printSummary(w io.Writer, s *trader.Status) {
	fmt.Fprintln(w, "UID", s.UID)
	fmt.Fprintln(w, "ProductID", s.ProductID)
	fmt.Fprintln(w, "ExchangeName", s.ExchangeName)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Budget", s.Budget.StringFixed(5))
	fmt.Fprintln(w, "Profit", s.Profit().StringFixed(5))
	fmt.Fprintln(w, "ReturnRate", s.ReturnRate().StringFixed(5))
	fmt.Fprintln(w, "AnnualReturnRate", s.AnnualReturnRate().StringFixed(5))
	fmt.Fprintln(w, "NumDays", s.NumDays().StringFixed(2))
	fmt.Fprintln(w, "NumBuys", s.NumBuys)
	fmt.Fprintln(w, "NumSells", s.NumSells)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "BoughtValue", s.BoughtValue.StringFixed(5), "Fees", s.BoughtFees.StringFixed(5), "Size", s.BoughtSize.StringFixed(5))
	fmt.Fprintln(w, "SoldValue", s.SoldValue.StringFixed(5), "Fees", s.SoldFees.StringFixed(5), "Size", s.SoldSize.StringFixed(5))
	fmt.Fprintln(w, "UnsoldValue", s.UnsoldValue.StringFixed(5), "Fees", s.UnsoldFees.StringFixed(5), "Size", s.UnsoldSize.StringFixed(5))
	fmt.Fprintln(w, "OversoldValue", s.OversoldValue.StringFixed(5), "Fees", s.OversoldFees.StringFixed(5), "Size", s.OversoldSize.StringFixed(5))
	fmt.Fprintln(w, "PremiumValue", s.PremiumValue.StringFixed(5), "Fees", s.PremiumFees.StringFixed(5))
	fmt.Fprintln(w, "OpenPremiumValue", s.OpenPremiumValue.StringFixed(5), "Fees", s.OpenPremiumFees.StringFixed(5))
}

// printGreelerState prints a greeler's mode, current epoch, pending flip
// and held contract, then one row per grid level.
func printGreelerState(w io.Writer, now time.Time, v *greeler.Greeler, gv *gobs.GreelerState) {
	cfg, progress := gv.V1.Config, gv.V1.Progress
	e := progress.Epochs[len(progress.Epochs)-1]
	fmt.Fprintln(w, "Mode", v.Mode())
	fmt.Fprintln(w, "Epoch", len(progress.Epochs)-1, "since", e.StartAt.Format(time.RFC3339))
	if e.PendingFlip == "" {
		fmt.Fprintln(w, "PendingFlip none")
	} else {
		fmt.Fprintln(w, "PendingFlip", e.PendingFlip, "since", e.PendingFlipAt.Format(time.RFC3339),
			"dwelled", now.Sub(e.PendingFlipAt).Round(time.Second), "of", cfg.DwellTime)
	}
	if id, known := v.HeldContract(); known && id != "" {
		fmt.Fprintln(w, "HeldContract", id)
	} else {
		fmt.Fprintln(w, "HeldContract none")
	}
	printOptions(w, gv.V1.Options)
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 1, ' ', tabwriter.AlignRight)
	fmt.Fprintf(tw, "Level\tSize\tBuy\tBuyCancel\tSell\tSellCancel\t\n")
	for i, p := range cfg.GridLevels {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t\n", i, p.Buy.Size.StringFixed(2),
			p.Buy.Price.StringFixed(4), p.Buy.Cancel.StringFixed(4), p.Sell.Price.StringFixed(4), p.Sell.Cancel.StringFixed(4))
	}
	tw.Flush()
}

// loadGreeler loads a greeler and its saved record.
func loadGreeler(ctx context.Context, r kv.Reader, uid string) (*greeler.Greeler, *gobs.GreelerState, error) {
	v, err := greeler.Load(ctx, uid, r)
	if err != nil {
		return nil, nil, fmt.Errorf("could not load greeler %q: %w", uid, err)
	}
	gv, err := kvutil.Get[gobs.GreelerState](ctx, r, path.Join(greeler.DefaultKeyspace, uid))
	if err != nil {
		return nil, nil, fmt.Errorf("could not load greeler %q state: %w", uid, err)
	}
	return v, gv, nil
}

func printOptions(w io.Writer, opts map[string]string) {
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintln(w, "Option", k+"="+opts[k])
	}
}
