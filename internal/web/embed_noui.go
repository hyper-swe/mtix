// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package web — placeholder when UI assets are not embedded.
// Built with the noui build tag, or when web/dist/ doesn't exist.
//
//go:build noui

package web

import (
	"fmt"
	"net/http"
)

// HasEmbeddedUI reports whether the built UI is embedded.
// Returns false in noui builds.
func HasEmbeddedUI() bool {
	return false
}

// DistFS returns an error because the UI is not embedded.
func DistFS() (http.FileSystem, error) {
	return nil, fmt.Errorf("UI not embedded: build with web/dist/ present")
}

// SPAHandler returns a placeholder handler when the UI is not embedded.
// Displays a message instructing the user to build the UI.
func SPAHandler() (http.Handler, error) {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, placeholderHTML)
	}), nil
}

const placeholderHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>mtix — UI Not Built</title>
  <style>
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      display: flex; align-items: center; justify-content: center;
      min-height: 100vh; margin: 0;
      background: #1A1A2E; color: #E8E8F0;
    }
    .card {
      text-align: center; padding: 3rem;
      border: 1px solid #333; border-radius: 12px;
      max-width: 480px;
    }
    h1 { font-size: 1.5rem; margin: 0 0 1rem; }
    p { color: #9CA3AF; line-height: 1.6; }
    code {
      background: #222244; padding: 0.25rem 0.5rem;
      border-radius: 4px; font-size: 0.875rem;
    }
  </style>
</head>
<body>
  <div class="card">
    <h1>mtix — UI Not Built</h1>
    <p>The web UI has not been compiled into this binary.</p>
    <p>To build the UI, run:</p>
    <p><code>cd web && npm install && npm run build</code></p>
    <p>Then rebuild the mtix binary.</p>
  </div>
</body>
</html>`
