// Copyright (c) 2026 Deepak Vankadaru

// Package web embeds the static web UI assets.
package web

import "embed"

//go:embed index.html ibkr
var FS embed.FS
