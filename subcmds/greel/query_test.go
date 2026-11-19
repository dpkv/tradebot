// Copyright (c) 2026 Deepak Vankadaru

package greel

import (
	"context"
	"strings"
	"testing"

	"github.com/visvasity/cli"
)

func runQuery(args ...string) error {
	cmds := []cli.Command{
		cli.NewGroup("greeler", "", new(GreelerQuery)),
		cli.NewGroup("greelladder", "", new(LadderQuery)),
	}
	return cli.Run(context.Background(), cmds, args)
}

func TestQuery(t *testing.T) {
	// sampleArgs: buys 150..158 by 2, sells +3, 50 shares each, no fees,
	// grid 5%, far 12%, hysteresis 2%.
	for _, tc := range []struct {
		group string
		want  []string
	}{
		// One greel: top sell 161, bottom buy 150. Put at spot >= 161/0.88,
		// back below 161/0.90; call at spot <= 150/1.12, back above 150/1.10.
		{"greeler", []string{
			"0      5 250.00    150.00      161.00 38500.00    15000.00      182.95    178.89       133.93    136.36",
			"level-4: size 50 buy 158.0000",
		}},
		// Two greels: 150-152 (100 shares) and 154-158 (150 shares).
		{"greelladder", []string{
			"0      2 100.00    150.00      155.00 15100.00    15000.00      176.14    172.22       133.93    136.36",
			"1      3 150.00    154.00      161.00 23400.00    15400.00      182.95    178.89       137.50    140.00",
			"Total budget: 38500.00",
			"Put exposure if every greel writes a put: 30400.00",
			"greel-1 level-2: size 50 buy 158.0000",
		}},
	} {
		args := append([]string{tc.group, "query"}, sampleArgs...)
		out, err := captureStdout(t, func() error { return runQuery(args...) })
		if err != nil {
			t.Fatalf("%s: %v", tc.group, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: output lacks %q:\n%s", tc.group, w, out)
			}
		}
	}
}

func TestQueryChecksTheConfig(t *testing.T) {
	args := append([]string{"greelladder", "query"}, sampleArgs[2:]...) // no --account
	if err := runQuery(args...); err == nil || !strings.Contains(err.Error(), "--account") {
		t.Errorf("err = %v, want missing --account", err)
	}
}
