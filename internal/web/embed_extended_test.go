// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !noui

package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmbed_DistFS_ReturnsValidFileSystem verifies DistFS returns a valid filesystem.
func TestEmbed_DistFS_ReturnsValidFileSystem(t *testing.T) {
	fs, err := DistFS()
	if err != nil {
		// If dist/ doesn't exist during testing, DistFS will error.
		// This is acceptable and expected behavior.
		t.Skipf("dist/ not embedded: %v", err)
	}
	require.NotNil(t, fs)
}

// TestEmbed_SPAHandler_ServesSPARoutes verifies non-file requests return index.html.
func TestEmbed_SPAHandler_ServesSPARoutes(t *testing.T) {
	handler, err := SPAHandler()
	if err != nil {
		t.Skipf("embed not available: %v", err)
	}
	if handler == nil {
		t.Skip("no SPA handler available")
	}

	// SPA routes must serve the entry point without a redirect.
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestEmbed_SPAHandler_RootServesIndex verifies "/" is served as index.html.
func TestEmbed_SPAHandler_RootServesIndex(t *testing.T) {
	handler, err := SPAHandler()
	if err != nil && !strings.Contains(err.Error(), "pattern") {
		t.Skipf("embed not available: %v", err)
	}
	require.NotNil(t, handler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	cacheControl := w.Header().Get("Cache-Control")
	assert.Contains(t, cacheControl, "no-cache")
}

// TestEmbed_CacheControl_EmptyPath verifies empty path gets no-cache headers.
func TestEmbed_CacheControl_EmptyPath(t *testing.T) {
	w := httptest.NewRecorder()
	setCacheHeaders(w, "")
	cc := w.Header().Get("Cache-Control")
	assert.Contains(t, cc, "no-cache", "empty path should get no-cache")
}

// TestEmbed_CacheControl_Fonts verifies font files get long-term caching.
func TestEmbed_CacheControl_Fonts(t *testing.T) {
	tests := []struct {
		path      string
		extension string
	}{
		{"assets/font-A1B2C3.woff", ".woff"},
		{"assets/font-X9Y8Z7.woff2", ".woff2"},
		{"assets/font-abc123.ttf", ".ttf"},
	}

	for _, tt := range tests {
		t.Run(tt.extension, func(t *testing.T) {
			w := httptest.NewRecorder()
			setCacheHeaders(w, tt.path)
			cc := w.Header().Get("Cache-Control")
			assert.Contains(t, cc, "max-age=31536000", "font %s should get 1-year cache", tt.path)
			assert.Contains(t, cc, "immutable")
		})
	}
}

// TestEmbed_CacheControl_Images verifies image files get 1-day caching.
func TestEmbed_CacheControl_Images(t *testing.T) {
	tests := []struct {
		path string
		name string
	}{
		{"mtix.png", ".png"},
		{"favicon.ico", ".ico"},
		{"logo.svg", ".svg"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			setCacheHeaders(w, tt.path)
			cc := w.Header().Get("Cache-Control")
			assert.Contains(t, cc, "max-age=86400", "image %s should get 1-day cache", tt.path)
		})
	}
}

// TestEmbed_CacheControl_UnknownExtension verifies unknown extensions get default cache.
func TestEmbed_CacheControl_UnknownExtension(t *testing.T) {
	w := httptest.NewRecorder()
	setCacheHeaders(w, "data.unknown")
	cc := w.Header().Get("Cache-Control")
	assert.Contains(t, cc, "max-age=3600", "unknown extension should get 1-hour default cache")
}

// TestEmbed_SetCacheHeadersPragma verifies Pragma header is set for index.html.
func TestEmbed_SetCacheHeadersPragma(t *testing.T) {
	w := httptest.NewRecorder()
	setCacheHeaders(w, "index.html")
	pragma := w.Header().Get("Pragma")
	assert.Equal(t, "no-cache", pragma)
}

// TestEmbed_HasEmbeddedUI_NoError verifies HasEmbeddedUI doesn't panic.
func TestEmbed_HasEmbeddedUI_NoError(t *testing.T) {
	// This function should always succeed without panicking.
	// The return value depends on whether dist/ is embedded.
	result := HasEmbeddedUI()
	assert.IsType(t, false, result)
}

// TestEmbed_SPAHandler_NonExistentFileServesSPA verifies 404 assets fall back to SPA.
func TestEmbed_SPAHandler_NonExistentFileServesSPA(t *testing.T) {
	handler, err := SPAHandler()
	if err != nil {
		t.Skipf("embed not available: %v", err)
	}
	if handler == nil {
		t.Skip("no SPA handler available")
	}

	req := httptest.NewRequest(http.MethodGet, "/nonexistent-route", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestEmbed_SPAHandler_DeepRouteServesSPA verifies deep SPA routes work.
func TestEmbed_SPAHandler_DeepRouteServesSPA(t *testing.T) {
	handler, err := SPAHandler()
	if err != nil {
		t.Skipf("embed not available: %v", err)
	}
	if handler == nil {
		t.Skip("no SPA handler available")
	}

	req := httptest.NewRequest(http.MethodGet, "/nodes/TEST-1.1.1/edit", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestEmbed_SPAHandler_DeepTicketsReturnIndex covers FR-9.1 without redirects or listings.
func TestEmbed_SPAHandler_DeepTicketsReturnIndex(t *testing.T) {
	handler, err := SPAHandler()
	require.NoError(t, err)
	index, err := distFS.ReadFile("dist/index.html")
	require.NoError(t, err)
	for _, path := range []string{"/ticket/TEST-1", "/ticket/TEST-1/", "/ticket/TEST-1.2?view=detail", "/assets", "/assets/"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			original := req.URL.String()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Empty(t, w.Header().Get("Location"))
			assert.Equal(t, string(index), w.Body.String())
			assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
			assert.Contains(t, w.Header().Get("Cache-Control"), "no-cache")
			assert.Equal(t, original, req.URL.String())
		})
	}
}

// TestEmbed_SPAHandler_StaticAssetsRemainFiles verifies exact bytes and asset caching.
func TestEmbed_SPAHandler_StaticAssetsRemainFiles(t *testing.T) {
	handler, err := SPAHandler()
	require.NoError(t, err)
	paths, err := fs.Glob(distFS, "dist/assets/*")
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			content, err := distFS.ReadFile(path)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, strings.TrimPrefix(path, "dist"), nil))
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, string(content), w.Body.String())
			assert.Contains(t, w.Header().Get("Cache-Control"), "immutable")
		})
	}
}

// TestEmbed_SPAHandler_UnknownAPIReturns404 keeps API misses out of client routing.
func TestEmbed_SPAHandler_UnknownAPIReturns404(t *testing.T) {
	handler, err := SPAHandler()
	require.NoError(t, err)
	for _, path := range []string{"/api", "/api/unknown", "/api/v1/unknown"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			assert.Equal(t, http.StatusNotFound, w.Code)
			assert.Empty(t, w.Header().Get("Location"))
			assert.NotContains(t, w.Body.String(), `<div id="root">`)
		})
	}
}
