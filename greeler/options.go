// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

var trues = []string{"true", "yes", "1"}
var falses = []string{"false", "no", "0"}

// SetOption sets retire or freeze. Like every trader.Trader option, it
// changes only while the greeler isn't running.
//
//   - retire=true: no new buys and no new wheel entries; the greeler ends
//     once every level is flat and its position, if any, has ended.
//   - freeze=grid: no new stock limiters; existing ones still finish.
//   - freeze=wheel: no new positions; an open one still runs to its end.
//   - freeze=all: both; freeze=none clears them.
func (v *Greeler) SetOption(opt, val string) (string, error) {
	switch key := strings.ToLower(opt); key {
	case "retire":
		return v.setRetireOption(val)
	case "freeze":
		return v.setFreezeOption(val)
	default:
		return "", fmt.Errorf("invalid/unsupported greeler option %q", key)
	}
}

func (v *Greeler) options() map[string]string {
	opts := make(map[string]string)
	if v.retireOpt {
		opts["retire"] = "true"
	}
	if v.freezeGridOpt || v.freezeWheelOpt {
		opts["freeze"] = v.currentFreezeValue()
	}
	return opts
}

// setRetireOption mirrors looper's: retire can be set but not unset with
// "false"; the returned "undo" value rolls it back.
func (v *Greeler) setRetireOption(val string) (string, error) {
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
			slog.Error("attempts to undo a retire operation when it is already false are unexpected (ignored)", "greeler", v)
		}
		return "undo", nil
	}
	return "", fmt.Errorf("invalid value %q for the retire option", value)
}

func (v *Greeler) currentFreezeValue() string {
	switch {
	case v.freezeGridOpt && v.freezeWheelOpt:
		return "all"
	case v.freezeGridOpt:
		return "grid"
	case v.freezeWheelOpt:
		return "wheel"
	default:
		return "none"
	}
}

// setFreezeOption sets the freeze to exactly the given value and returns
// "undo:<previous>".
func (v *Greeler) setFreezeOption(val string) (string, error) {
	current := v.currentFreezeValue()
	value := strings.TrimPrefix(strings.ToLower(val), "undo:")
	switch value {
	case "", current:
		return "", nil
	case "grid":
		v.freezeGridOpt, v.freezeWheelOpt = true, false
	case "wheel":
		v.freezeGridOpt, v.freezeWheelOpt = false, true
	case "all":
		v.freezeGridOpt, v.freezeWheelOpt = true, true
	case "none":
		v.freezeGridOpt, v.freezeWheelOpt = false, false
	default:
		return "", fmt.Errorf("invalid value %q for the freeze option", val)
	}
	return "undo:" + current, nil
}
