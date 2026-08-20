// Copyright (c) 2024 BVK Chaitanya

package looper

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

var trues = []string{"true", "yes", "1"}
var falses = []string{"false", "no", "0"}

func (v *Looper) SetOption(opt, val string) (string, error) {
	switch key := strings.ToLower(opt); key {
	case "retire":
		return v.setRetireOption(key, val)
	case "freeze":
		return v.setFreezeOption(key, val)
	default:
		return "", fmt.Errorf("invalid/unsupported looper option %q", key)
	}
}

func (v *Looper) setRetireOption(opt, val string) (string, error) {
	// This retire option can be set to "true", but cannot be unset after the job
	// has started already. So, we return "undo" to indicate the undo action
	// separate from a "false" value.

	value := strings.ToLower(val)
	if slices.Contains(trues, value) {
		if !v.retireOpt {
			v.retireOpt = true
			return "undo", nil
		}
		return "true", nil
	}

	if slices.Contains(falses, value) {
		if v.retireOpt {
			return "", fmt.Errorf("retire option cannot be undone")
		}
		return "false", nil
	}

	if value == "undo" {
		if v.retireOpt {
			v.retireOpt = false
		} else {
			slog.Error("attempts to undo a retire operation when it is already false are unexpected (ignored)")
		}
		return "undo", nil
	}
	return "", fmt.Errorf("invalid value %q for the retire-option", value)
}

func (v *Looper) currentFreezeValue() string {
	switch {
	case v.freezeBuysOpt && v.freezeSellsOpt:
		return "both"
	case v.freezeBuysOpt:
		return "buys"
	case v.freezeSellsOpt:
		return "sells"
	default:
		return "none"
	}
}

func (v *Looper) setFreezeOption(opt, val string) (string, error) {
	current := v.currentFreezeValue()

	// Handle undo prefix if it exists.
	value := strings.ToLower(val)
	if strings.HasPrefix(value, "undo:") {
		value = strings.TrimPrefix(value, "undo:")
	}

	// No change.
	if value == "" || value == current {
		return "", nil
	}

	switch value {
	case "buy", "buys":
		v.freezeBuysOpt, v.freezeSellsOpt = true, false
	case "sell", "sells":
		v.freezeBuysOpt, v.freezeSellsOpt = false, true
	case "both":
		v.freezeBuysOpt, v.freezeSellsOpt = true, true
	case "none":
		v.freezeBuysOpt, v.freezeSellsOpt = false, false
	default:
		return "", fmt.Errorf("invalid value %q for the freeze-option", value)
	}
	return "undo:" + current, nil
}
