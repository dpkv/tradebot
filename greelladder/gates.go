// Copyright (c) 2026 Deepak Vankadaru

package greelladder

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// Risk gates (greel/risk-gates-story.md): the ladder admits each flip to
// wheel mode against two limits of its own, max-contracts and
// max-put-exposure. Zero means no limit. Nothing here is persisted besides
// the limits, which are ladder options.

const (
	maxContractsOpt   = "max-contracts"
	maxPutExposureOpt = "max-put-exposure"
)

// reservation is room admitted to a greeler that doesn't report wheel mode
// yet. It lapses after claimTTL, in case the flip fails first.
type reservation struct {
	optionType string
	exposure   decimal.Decimal
	at         time.Time
}

// admitFor is the admission hook for one greeler.
func (v *GreelLadder) admitFor(uid string) func(optionType string, exposure decimal.Decimal) bool {
	return func(optionType string, exposure decimal.Decimal) bool {
		return v.admit(uid, optionType, exposure)
	}
}

// admit reports whether uid may flip to wheel mode to write one contract
// of optionType with exposure, counting what its siblings write or have
// just been admitted for, and reserves the room if so.
func (v *GreelLadder) admit(uid, optionType string, exposure decimal.Decimal) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	now := v.now()
	contracts := 1
	var putExposure decimal.Decimal
	if optionType == "PUT" {
		putExposure = exposure
	}
	for _, s := range v.siblings {
		sid := s.UID()
		if sid == uid {
			continue
		}
		t, e := s.Commitment()
		if t == "" {
			r, ok := v.reservations[sid]
			if !ok || now.Sub(r.at) >= claimTTL {
				continue
			}
			t, e = r.optionType, r.exposure
		}
		contracts++
		if t == "PUT" {
			putExposure = putExposure.Add(e)
		}
	}

	reason := ""
	switch {
	case v.maxContracts > 0 && contracts > v.maxContracts:
		reason = fmt.Sprintf("it would hold %d contracts, over its limit of %d", contracts, v.maxContracts)
	case optionType == "PUT" && v.maxPutExposure.IsPositive() && putExposure.GreaterThan(v.maxPutExposure):
		reason = fmt.Sprintf("its put exposure would be %s, over its limit of %s", putExposure.StringFixed(2), v.maxPutExposure.StringFixed(2))
	}
	if reason != "" {
		if !v.blocked[uid] {
			v.blocked[uid] = true
			slog.Warn("risk gates hold a greeler's wheel flip", "greelladder", v, "greeler", uid, "option-type", optionType, "reason", reason)
			v.alert("Greel ladder %s holds greeler %s from writing a %s on %s: %s.", v.uid, uid, optionType, v.cfg.ProductID, reason)
		}
		return false
	}
	delete(v.blocked, uid)
	v.reservations[uid] = &reservation{optionType: optionType, exposure: exposure, at: now}
	return true
}

// alert queues a messenger alert for Run to send. It never blocks; an
// alert that doesn't fit is only logged.
func (v *GreelLadder) alert(format string, args ...any) {
	select {
	case v.alertCh <- fmt.Sprintf(format, args...):
	default:
		slog.Warn("dropped a greel ladder alert", "greelladder", v, "alert", fmt.Sprintf(format, args...))
	}
}

// setLimitOption sets max-contracts or max-put-exposure and returns the
// previous value, which undoes it.
func (v *GreelLadder) setLimitOption(key, val string) (string, error) {
	switch key {
	case maxContractsOpt:
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return "", fmt.Errorf("invalid value %q for the %s option", val, key)
		}
		prev := strconv.Itoa(v.maxContracts)
		v.maxContracts = n
		return prev, nil
	case maxPutExposureOpt:
		d, err := decimal.NewFromString(val)
		if err != nil || d.IsNegative() {
			return "", fmt.Errorf("invalid value %q for the %s option", val, key)
		}
		prev := v.maxPutExposure.String()
		v.maxPutExposure = d
		return prev, nil
	}
	return "", fmt.Errorf("invalid/unsupported greel ladder option %q", key)
}

// options are the ladder's own options, as saved.
func (v *GreelLadder) options() map[string]string {
	opts := make(map[string]string)
	if v.maxContracts > 0 {
		opts[maxContractsOpt] = strconv.Itoa(v.maxContracts)
	}
	if v.maxPutExposure.IsPositive() {
		opts[maxPutExposureOpt] = v.maxPutExposure.String()
	}
	if len(opts) == 0 {
		return nil
	}
	return opts
}
