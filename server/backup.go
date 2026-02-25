// Copyright (c) 2026 Deepak Vankadaru

package server

import (
	"context"
	"net/http"
	"time"

	"github.com/bvk/tradebot/kvutil"
	"github.com/bvkgo/kv"
)

func (s *Server) doBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	filename := "tradebot-backup-" + time.Now().UTC().Format("2006-01-02") + ".gob"
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")

	exportFn := func(ctx context.Context, reader kv.Reader) error {
		return kvutil.Export(ctx, reader, w)
	}
	if err := kv.WithReader(r.Context(), s.db, exportFn); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
