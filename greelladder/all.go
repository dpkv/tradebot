// Copyright (c) 2026 Deepak Vankadaru

package greelladder

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/bvkgo/kv"
)

// LoadFunc loads every record in DefaultKeyspace that pickf accepts, as
// looper.LoadFunc does.
func LoadFunc(ctx context.Context, r kv.Reader, pickf func(string) bool) ([]*GreelLadder, error) {
	const MinUUID = "00000000-0000-0000-0000-000000000000"
	const MaxUUID = "ffffffff-ffff-ffff-ffff-ffffffffffff"

	begin := path.Join(DefaultKeyspace, MinUUID)
	end := path.Join(DefaultKeyspace, MaxUUID)

	it, err := r.Ascend(ctx, begin, end)
	if err != nil {
		return nil, err
	}
	defer kv.Close(it)

	var ladders []*GreelLadder
	for k, _, err := it.Fetch(ctx, false); err == nil; k, _, err = it.Fetch(ctx, true) {
		if pickf != nil && !pickf(k) {
			continue
		}
		v, err := Load(ctx, strings.TrimPrefix(k, DefaultKeyspace), r)
		if err != nil {
			return nil, err
		}
		ladders = append(ladders, v)
	}

	if _, _, err := it.Fetch(ctx, false); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return ladders, nil
}
