// Copyright (c) 2026 Deepak Vankadaru

// Package web embeds the static web UI assets.
package web

import "embed"

//go:embed index.html icon.svg ibkr
var FS embed.FS
