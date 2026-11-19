// Copyright (c) 2026 Deepak Vankadaru

package greel

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bvk/tradebot/greeler"
	"github.com/bvk/tradebot/greelladder"
	"github.com/bvk/tradebot/job"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvkgo/kv"
	"github.com/bvkgo/kv/kvmemdb"
	"github.com/google/uuid"
	"github.com/visvasity/cli"
)

// captureStdout runs f and returns what it printed.
func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	ferr := f()
	w.Close()
	os.Stdout = old
	return <-done, ferr
}

// testBackup saves one greeler and one two-greel ladder into a database
// backup file.
func testBackup(t *testing.T) (file, greelerUID, ladderUID string) {
	t.Helper()
	cfg, err := parse(t, sampleArgs).Config()
	if err != nil {
		t.Fatal(err)
	}
	g, err := greeler.New(uuid.NewString(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	bands, err := greelladder.Cut(cfg)
	if err != nil {
		t.Fatal(err)
	}
	l, err := greelladder.New(uuid.NewString(), cfg.AccountName, cfg.ProductID, bands)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.SetOption("max-contracts", "2"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db := kvmemdb.New()
	runner := job.NewRunner(db)
	save := func(ctx context.Context, rw kv.ReadWriter) error {
		if err := g.Save(ctx, rw); err != nil {
			return err
		}
		if err := runner.Add(ctx, rw, g.UID(), "Greeler"); err != nil {
			return err
		}
		if err := l.Save(ctx, rw); err != nil {
			return err
		}
		return runner.Add(ctx, rw, l.UID(), "GreelLadder")
	}
	if err := kv.WithReadWriter(ctx, db, save); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(t.TempDir(), "backup")
	if err := kvutil.BackupDB(ctx, db, file); err != nil {
		t.Fatal(err)
	}
	return file, g.UID(), l.UID()
}

func runViews(args ...string) error {
	cmds := []cli.Command{
		cli.NewGroup("greeler", "", new(GreelerList), new(GreelerGet), new(GreelerPrint)),
		cli.NewGroup("greelladder", "", new(LadderList), new(LadderGet), new(LadderPrint)),
	}
	return cli.Run(context.Background(), cmds, args)
}

func TestViews(t *testing.T) {
	file, gid, lid := testBackup(t)
	for _, tc := range []struct {
		args []string
		want []string
		not  []string
	}{
		{[]string{"greeler", "list"}, []string{gid}, []string{"greeler-000000"}},
		{[]string{"greeler", "list", "--all"}, []string{gid, lid + "/greeler-000000", lid + "/greeler-000001"}, nil},
		{[]string{"greelladder", "list"}, []string{lid}, nil},
		{[]string{"greeler", "get", gid}, []string{"Mode grid", "PendingFlip none", "HeldContract none", "Level"}, nil},
		{[]string{"greeler", "print", gid}, []string{`"GridLevels"`, `"Epochs"`}, nil},
		{[]string{"greelladder", "get", lid}, []string{"Greels 2", "Contracts 0 limit 2", "PutExposure 0.00 limit none", "Option max-contracts=2", "=== greel-1 " + lid + "/greeler-000001"}, nil},
		{[]string{"greelladder", "print", lid}, []string{`"GreelerIDs"`, `"max-contracts": "2"`}, nil},
	} {
		args := append(tc.args[:2:2], "--from-backup", file)
		args = append(args, tc.args[2:]...)
		out, err := captureStdout(t, func() error { return runViews(args...) })
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
