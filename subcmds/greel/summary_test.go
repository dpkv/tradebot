// Copyright (c) 2026 Deepak Vankadaru

package greel

import (
	"context"
	"strings"
	"testing"

	"github.com/visvasity/cli"
)

func runSummary(args ...string) error {
	cmds := []cli.Command{
		cli.NewGroup("greeler", "", new(GreelerSummary)),
		cli.NewGroup("greelladder", "", new(LadderSummary)),
	}
	return cli.Run(context.Background(), cmds, args)
}

func TestSummary(t *testing.T) {
	file, gid, lid := testBackup(t)
	for _, tc := range []struct {
		args []string
		want []string
		not  []string
	}{
		// New jobs have no fills, so they are uninteresting by default.
		{[]string{"greeler", "summary"}, nil, []string{gid}},
		{[]string{"greeler", "summary", "--skip-uninteresting=false", "-table-columns", "Name,Budget,LowestBuyPrice,HighestSellPrice,PremiumValue"},
			[]string{gid, "38500.000", "150.000", "161.000", "PremiumValue"}, []string{lid}},
		{[]string{"greelladder", "summary", "--skip-uninteresting=false", "-f", "json"},
			[]string{`"UID": "` + lid + `"`, `"LowestBuyPrice": "150"`, `"HighestSellPrice": "161"`, `"Typename": "GreelLadder"`}, []string{gid}},
		{[]string{"greelladder", "summary", "--skip-uninteresting=false", "-f", "template", "--template-form", "{{.Name}} {{.Budget}}", lid},
			[]string{lid + " 38500"}, nil},
		{[]string{"greelladder", "summary", "--skip-uninteresting=false", "nonesuch"}, nil, []string{lid}},
	} {
		args := append(tc.args[:2:2], "--from-backup", file)
		args = append(args, tc.args[2:]...)
		out, err := captureStdout(t, func() error { return runSummary(args...) })
		if err != nil {
			t.Errorf("%v: %v", tc.args, err)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Errorf("%v: output lacks %q:\n%s", tc.args, w, out)
			}
		}
		for _, w := range tc.not {
			if strings.Contains(out, w) {
				t.Errorf("%v: output has %q:\n%s", tc.args, w, out)
			}
		}
	}
}
