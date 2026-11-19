// Copyright (c) 2026 Deepak Vankadaru

package greel

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/visvasity/cli"
)

// runCLI runs args through the cli package, as main does: it sets flag
// values without flag.FlagSet.Parse, which the required-flag check must see.
func runCLI(args ...string) error {
	cmds := []cli.Command{
		cli.NewGroup("greeler", "", new(GreelerAdd)),
		cli.NewGroup("greelladder", "", new(LadderAdd)),
	}
	return cli.Run(context.Background(), cmds, args)
}

func TestAddDryRun(t *testing.T) {
	args := append([]string{"greeler", "add", "--dry-run", "--name", "x", "--option", "freeze=wheel"}, sampleArgs...)
	if err := runCLI(args...); err != nil {
		t.Errorf("greeler add: %v", err)
	}
	args = append([]string{"greelladder", "add", "--dry-run", "--name", "x", "--max-contracts", "2", "--max-put-exposure", "50000"}, sampleArgs...)
	if err := runCLI(args...); err != nil {
		t.Errorf("greelladder add: %v", err)
	}
}

func TestAddDryRunRejects(t *testing.T) {
	without := func(name string) []string {
		var args []string
		for i := 0; i < len(sampleArgs); i += 2 {
			if sampleArgs[i] != name {
				args = append(args, sampleArgs[i], sampleArgs[i+1])
			}
		}
		return args
	}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no name":         {append([]string{"greeler", "add", "--dry-run"}, sampleArgs...), "--name"},
		"missing knob":    {append([]string{"greeler", "add", "--dry-run", "--name", "x"}, without("--reprice-step")...), "--reprice-step"},
		"bad option":      {append([]string{"greeler", "add", "--dry-run", "--name", "x", "--option", "bogus=1"}, sampleArgs...), "bogus"},
		"bad limit":       {append([]string{"greelladder", "add", "--dry-run", "--name", "x", "--max-contracts", "-1"}, sampleArgs...), "max-contracts"},
		"limit twice":     {append([]string{"greelladder", "add", "--dry-run", "--name", "x", "--max-contracts", "1", "--option", "max-contracts=2"}, sampleArgs...), "both"},
		"ladder no knobs": {append([]string{"greelladder", "add", "--dry-run", "--name", "x"}, without("--target-delta")...), "--target-delta"},
	} {
		err := runCLI(slices.Clone(tc.args)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming %s", name, err, tc.want)
		}
	}
}

func TestLadderBands(t *testing.T) {
	c := new(LadderAdd)
	_, fset, _ := c.Command()
	// sampleArgs is 150..158 by 2 at 50 shares: five levels, two greels.
	if err := fset.Parse(sampleArgs); err != nil {
		t.Fatal(err)
	}
	bands, err := c.bands()
	if err != nil {
		t.Fatal(err)
	}
	if len(bands) != 2 || len(bands[0].GridLevels) != 2 || len(bands[1].GridLevels) != 3 {
		t.Errorf("bands = %d, want 2 of 2 and 3 levels", len(bands))
	}
}
