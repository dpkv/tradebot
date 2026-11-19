// Copyright (c) 2026 Deepak Vankadaru

package greel

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/bvk/tradebot/gobs"
)

// optionsValue collects repeated --option key=value flags.
type optionsValue map[string]string

func (v optionsValue) String() string {
	var kvs []string
	for k, val := range v {
		kvs = append(kvs, k+"="+val)
	}
	sort.Strings(kvs)
	return strings.Join(kvs, ",")
}

func (v optionsValue) Set(s string) error {
	k, val, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("option %q is not in key=value form", s)
	}
	if _, ok := v[k]; ok {
		return fmt.Errorf("option %q is given twice", k)
	}
	v[k] = val
	return nil
}

// printLevels prints one line per grid level, prefixed with prefix.
func printLevels(prefix string, cfg *gobs.GreelConfig) {
	fprintLevels(os.Stdout, prefix, cfg)
}

func fprintLevels(w io.Writer, prefix string, cfg *gobs.GreelConfig) {
	for i, p := range cfg.GridLevels {
		fmt.Fprintf(w, "%slevel-%d: size %s buy %s (cancel %s) sell %s (cancel %s)\n", prefix, i,
			p.Buy.Size, p.Buy.Price.StringFixed(4), p.Buy.Cancel.StringFixed(4), p.Sell.Price.StringFixed(4), p.Sell.Cancel.StringFixed(4))
	}
}
