// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package web embeds the built UI assets into the Go binary per FR-9.1.
// Uses //go:embed to include the Vite build output from web/dist/.
// Provides an http.FileSystem for serving the SPA, with SPA routing
// (non-API requests return index.html for client-side routing).
//
//go:build !noui

package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// HasEmbeddedUI reports whether the built UI is embedded.
// Returns true when built with web/dist/ present.
func HasEmbeddedUI() bool {
	_, err := distFS.ReadFile("dist/index.html")
	return err == nil
}

// DistFS returns an http.FileSystem rooted at dist/.
// Used by the HTTP server to mount as a catch-all after API routes.
func DistFS() (http.FileSystem, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	return http.FS(sub), nil
}

// SPAHandler returns an http.Handler that serves the embedded SPA.
// Static files are served directly. Non-file requests (SPA routes)
// return index.html for client-side routing.
//
// Cache-Control policy per FR-9.1:
//   - Hashed assets (e.g., index-CKm2CZaK.js): max-age=31536000 (1 year)
//   - index.html: no-cache (always revalidate)
func SPAHandler() (http.Handler, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}

	fsys := http.FS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}

		// Try to open the requested file.
		cleanPath := strings.TrimPrefix(path, "/")
		if _, err := sub.Open(cleanPath); err != nil {
			// File not found — serve index.html for SPA routing.
			serveIndexHTML(w, r, fsys)
			return
		}

		// Set Cache-Control based on file type.
		setCacheHeaders(w, cleanPath)

		// Serve the static file.
		http.FileServer(fsys).ServeHTTP(w, r)
	}), nil
}

// serveIndexHTML serves the index.html file with no-cache headers.
func serveIndexHTML(w http.ResponseWriter, r *http.Request, fsys http.FileSystem) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	r.URL.Path = "/index.html"
	http.FileServer(fsys).ServeHTTP(w, r)
}

// setCacheHeaders sets appropriate Cache-Control headers.
// Hashed assets get immutable long-cache, index.html gets no-cache.
func setCacheHeaders(w http.ResponseWriter, path string) {
	if path == "index.html" || path == "" {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		return
	}

	ext := filepath.Ext(path)
	switch ext {
	case ".js", ".css", ".woff2", ".woff", ".ttf":
		// Hashed assets — cache for 1 year per FR-9.1.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case ".svg", ".png", ".ico":
		// Static images — cache for 1 day.
		w.Header().Set("Cache-Control", "public, max-age=86400")
	default:
		// Default: short cache.
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
}
