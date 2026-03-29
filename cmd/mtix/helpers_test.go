// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// init.go helper tests — addToGitignore, findTemplateDir
// ============================================================================

// TestAddToGitignore_NewFile_CreatesWithEntry verifies gitignore creation.
func TestAddToGitignore_NewFile_CreatesWithEntry(t *testing.T) {
	tmpDir := t.TempDir()

	addToGitignore(tmpDir, "docs/")

	content, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "docs/\n")
}

// TestAddToGitignore_ExistingFile_AppendsEntry verifies append behavior.
func TestAddToGitignore_ExistingFile_AppendsEntry(t *testing.T) {
	tmpDir := t.TempDir()
	gitignorePath := filepath.Join(tmpDir, ".gitignore")
	require.NoError(t, os.WriteFile(gitignorePath, []byte("build/\n"), 0o644))

	addToGitignore(tmpDir, "docs/")

	content, err := os.ReadFile(gitignorePath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "build/\n")
	assert.Contains(t, string(content), "docs/\n")
}

// TestAddToGitignore_AlreadyPresent_NoChange verifies no duplicate entries.
func TestAddToGitignore_AlreadyPresent_NoChange(t *testing.T) {
	tmpDir := t.TempDir()
	gitignorePath := filepath.Join(tmpDir, ".gitignore")
	require.NoError(t, os.WriteFile(gitignorePath, []byte("docs/\n"), 0o644))

	addToGitignore(tmpDir, "docs/")

	content, err := os.ReadFile(gitignorePath)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(content), "docs/"))
}

// TestAddToGitignore_FileWithoutTrailingNewline_AddsNewline verifies newline insertion.
func TestAddToGitignore_FileWithoutTrailingNewline_AddsNewline(t *testing.T) {
	tmpDir := t.TempDir()
	gitignorePath := filepath.Join(tmpDir, ".gitignore")
	require.NoError(t, os.WriteFile(gitignorePath, []byte("build/"), 0o644))

	addToGitignore(tmpDir, "docs/")

	content, err := os.ReadFile(gitignorePath)
	require.NoError(t, err)
	// Should have newline between existing content and new entry.
	assert.Equal(t, "build/\ndocs/\n", string(content))
}

// TestGenerateInitDocs_ProducesFiles verifies embedded template doc generation.
func TestGenerateInitDocs_ProducesFiles(t *testing.T) {
	docsDir := filepath.Join(t.TempDir(), ".mtix", "docs")
	results := generateInitDocs(docsDir, "TEST", "0.1.0-test")
	assert.NotEmpty(t, results, "should produce doc files from embedded templates")
}

// TestGenerateInitDocs_OutputDirCreated verifies output dir is auto-created.
func TestGenerateInitDocs_OutputDirCreated(t *testing.T) {
	docsDir := filepath.Join(t.TempDir(), ".mtix", "docs")
	_ = generateInitDocs(docsDir, "TEST", "0.1.0-test")
	_, err := os.Stat(docsDir)
	assert.NoError(t, err, "docs directory should be created")
}

// TestRunDocsGenerate_UsesCorrectOutputDir verifies docs are written to .mtix/docs/ per FR-13.2.
func TestRunDocsGenerate_UsesCorrectOutputDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Change working directory to tmpDir for the duration of this test.
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	// Create .mtix dir to simulate initialized project.
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".mtix"), 0o755))

	err = runDocsGenerate(false)
	require.NoError(t, err)

	// Verify files are in .mtix/docs/, not docs/.
	mtixDocsDir := filepath.Join(tmpDir, ".mtix", "docs")
	_, err = os.Stat(mtixDocsDir)
	assert.NoError(t, err, ".mtix/docs/ should exist")

	rootDocsDir := filepath.Join(tmpDir, "docs")
	_, err = os.Stat(rootDocsDir)
	assert.True(t, os.IsNotExist(err), "docs/ at project root should NOT exist")
}

// TestRunDocsGenerate_RejectsSymlink verifies symlink detection per security audit.
func TestRunDocsGenerate_RejectsSymlink(t *testing.T) {
	tmpDir := t.TempDir()

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	// Create .mtix dir.
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".mtix"), 0o755))

	// Create a symlink at .mtix/docs pointing to /tmp.
	target := t.TempDir()
	symlinkPath := filepath.Join(tmpDir, ".mtix", "docs")
	require.NoError(t, os.Symlink(target, symlinkPath))

	// runDocsGenerate MUST reject symlink.
	err = runDocsGenerate(false)
	require.Error(t, err, "should reject symlink at docs directory")
	assert.Contains(t, err.Error(), "symlink")
}

// TestRunInit_DoesNotAddDocsToGitignore verifies init no longer adds docs/ to .gitignore per FR-13.2.
func TestRunInit_DoesNotAddDocsToGitignore(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a .gitignore with some content.
	gitignorePath := filepath.Join(tmpDir, ".gitignore")
	require.NoError(t, os.WriteFile(gitignorePath, []byte("build/\n"), 0o644))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	// Run init.
	err = runInit("TEST")
	require.NoError(t, err)

	// Verify docs/ was NOT added to .gitignore.
	content, err := os.ReadFile(gitignorePath)
	require.NoError(t, err)
	assert.NotContains(t, string(content), "docs/", ".gitignore should NOT contain docs/")
}

// TestRunInit_DocsGeneratedInMtixDocs verifies init writes docs to .mtix/docs/.
func TestRunInit_DocsGeneratedInMtixDocs(t *testing.T) {
	tmpDir := t.TempDir()

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	err = runInit("TEST")
	require.NoError(t, err)

	// Verify docs are in .mtix/docs/.
	mtixDocsDir := filepath.Join(tmpDir, ".mtix", "docs")
	_, err = os.Stat(mtixDocsDir)
	assert.NoError(t, err, ".mtix/docs/ should exist after init")

	// Verify docs/ at root does NOT exist.
	rootDocsDir := filepath.Join(tmpDir, "docs")
	_, err = os.Stat(rootDocsDir)
	assert.True(t, os.IsNotExist(err), "docs/ at project root should NOT exist after init")
}

// ============================================================================
// routing.go tests — handleRouteResponse, routeStandardCommand
// ============================================================================

// TestHandleRouteResponse_SuccessJSON_PrintsBody verifies successful response.
func TestHandleRouteResponse_SuccessJSON_PrintsBody(t *testing.T) {
	body := `{"id": "PROJ-1", "status": "open"}`
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	// Capture stdout.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	resultErr := handleRouteResponse(resp)

	_ = w.Close()
	os.Stdout = oldStdout

	output, _ := io.ReadAll(r)

	assert.NoError(t, resultErr)
	assert.Contains(t, string(output), "PROJ-1")
}

// TestHandleRouteResponse_ErrorWithJSON_ReturnsServerError verifies error handling.
func TestHandleRouteResponse_ErrorWithJSON_ReturnsServerError(t *testing.T) {
	errBody := `{"error": "node not found"}`
	resp := &http.Response{
		StatusCode: 404,
		Body:       io.NopCloser(strings.NewReader(errBody)),
	}

	err := handleRouteResponse(resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "node not found")
}

// TestHandleRouteResponse_ErrorWithPlainText_ReturnsStatusAndBody verifies plain error.
func TestHandleRouteResponse_ErrorWithPlainText_ReturnsStatusAndBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(strings.NewReader("internal failure")),
	}

	err := handleRouteResponse(resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.Contains(t, err.Error(), "internal failure")
}

// TestHandleRouteResponse_ErrorWithEmptyJSON_ReturnsFallback verifies empty error JSON.
func TestHandleRouteResponse_ErrorWithEmptyJSON_ReturnsFallback(t *testing.T) {
	resp := &http.Response{
		StatusCode: 400,
		Body:       io.NopCloser(strings.NewReader(`{"error": ""}`)),
	}

	err := handleRouteResponse(resp)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

// TestRouteStandardCommand_ShowNoArgs_ReturnsError verifies missing args.
func TestRouteStandardCommand_ShowNoArgs_ReturnsError(t *testing.T) {
	cmd := newShowCmd()
	err := routeStandardCommand(cmd, []string{}, 8377)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "node ID required")
}

// TestRouteStandardCommand_SearchNoArgs_ReturnsError verifies missing search query.
func TestRouteStandardCommand_SearchNoArgs_ReturnsError(t *testing.T) {
	cmd := newSearchCmd()
	err := routeStandardCommand(cmd, []string{}, 8377)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "search query required")
}

// TestRouteStandardCommand_TreeNoArgs_ReturnsError verifies missing tree args.
func TestRouteStandardCommand_TreeNoArgs_ReturnsError(t *testing.T) {
	cmd := newTreeCmd()
	err := routeStandardCommand(cmd, []string{}, 8377)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "node ID required")
}

// TestRouteStandardCommand_UnsupportedCommand_ReturnsError verifies unsupported routing.
func TestRouteStandardCommand_UnsupportedCommand_ReturnsError(t *testing.T) {
	cmd := newCommentCmd()
	err := routeStandardCommand(cmd, []string{"id", "text"}, 8377)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not supported for server routing")
}

// TestRouteStandardCommand_ListWithServer_RoutesCorrectly verifies routing to mock server.
func TestRouteStandardCommand_ListWithServer_RoutesCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/nodes", r.URL.Path)
		assert.Equal(t, "mtix", r.Header.Get("X-Requested-With"))

		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"nodes": []any{}, "total": 0}
		data, _ := json.Marshal(resp)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	// Extract port from test server.
	parts := strings.Split(srv.URL, ":")
	portStr := parts[len(parts)-1]

	cmd := newListCmd()

	// Capture stdout.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	var port int
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}

	resultErr := routeStandardCommand(cmd, []string{}, port)

	_ = w.Close()
	os.Stdout = oldStdout
	output, _ := io.ReadAll(r)

	assert.NoError(t, resultErr)
	assert.Contains(t, string(output), "nodes")
}

// TestRouteStandardCommand_ShowWithServer_RoutesCorrectly verifies show routing.
func TestRouteStandardCommand_ShowWithServer_RoutesCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Contains(t, r.URL.Path, "/api/v1/nodes/PROJ-1")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"PROJ-1"}`))
	}))
	defer srv.Close()

	parts := strings.Split(srv.URL, ":")
	portStr := parts[len(parts)-1]
	var port int
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}

	cmd := newShowCmd()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	resultErr := routeStandardCommand(cmd, []string{"PROJ-1"}, port)

	_ = w.Close()
	os.Stdout = oldStdout
	output, _ := io.ReadAll(r)

	assert.NoError(t, resultErr)
	assert.Contains(t, string(output), "PROJ-1")
}

// TestRouteAdminCommand_BackupWithServer_RoutesCorrectly verifies admin routing.
func TestRouteAdminCommand_BackupWithServer_RoutesCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/admin/backup", r.URL.Path)

		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	parts := strings.Split(srv.URL, ":")
	portStr := parts[len(parts)-1]
	var port int
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}

	route := adminRoutes["backup"]

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	resultErr := routeAdminCommand(route, port)

	_ = w.Close()
	os.Stdout = oldStdout
	output, _ := io.ReadAll(r)

	assert.NoError(t, resultErr)
	assert.Contains(t, string(output), "ok")
}

// TestRouteAdminCommand_GETEndpoint_UsesGET verifies GET admin commands.
func TestRouteAdminCommand_GETEndpoint_UsesGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/admin/verify", r.URL.Path)

		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"verified":true}`))
	}))
	defer srv.Close()

	parts := strings.Split(srv.URL, ":")
	portStr := parts[len(parts)-1]
	var port int
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}

	route := adminRoutes["verify"]

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	resultErr := routeAdminCommand(route, port)

	_ = w.Close()
	os.Stdout = oldStdout
	output, _ := io.ReadAll(r)

	assert.NoError(t, resultErr)
	assert.Contains(t, string(output), "verified")
}

// TestRouteAdminCommand_ServerDown_ReturnsError verifies connection error.
func TestRouteAdminCommand_ServerDown_ReturnsError(t *testing.T) {
	route := adminRoutes["gc"]
	// Port 1 should not have a server.
	err := routeAdminCommand(route, 1)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "route to server")
}

// TestRouteToServer_AdminCommand_RoutesToAdmin verifies admin detection.
func TestRouteToServer_AdminCommand_RoutesToAdmin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/admin/gc", r.URL.Path)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	parts := strings.Split(srv.URL, ":")
	portStr := parts[len(parts)-1]
	var port int
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}

	cmd := newGCCmd()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	resultErr := routeToServer(cmd, []string{}, port)

	_ = w.Close()
	os.Stdout = oldStdout
	_, _ = io.ReadAll(r)

	assert.NoError(t, resultErr)
}

// TestRouteStandardCommand_StatsWithServer_RoutesCorrectly verifies stats routing.
func TestRouteStandardCommand_StatsWithServer_RoutesCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/stats", r.URL.Path)

		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"total":5}`))
	}))
	defer srv.Close()

	parts := strings.Split(srv.URL, ":")
	portStr := parts[len(parts)-1]
	var port int
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}

	cmd := newStatsCmd()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	resultErr := routeStandardCommand(cmd, []string{}, port)

	_ = w.Close()
	os.Stdout = oldStdout
	output, _ := io.ReadAll(r)

	assert.NoError(t, resultErr)
	assert.Contains(t, string(output), "total")
}

// TestRouteStandardCommand_SearchWithServer_RoutesCorrectly verifies search routing.
func TestRouteStandardCommand_SearchWithServer_RoutesCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Contains(t, r.URL.Path, "/api/v1/nodes/search")
		assert.Equal(t, "test-query", r.URL.Query().Get("q"))

		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	defer srv.Close()

	parts := strings.Split(srv.URL, ":")
	portStr := parts[len(parts)-1]
	var port int
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}

	cmd := newSearchCmd()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	resultErr := routeStandardCommand(cmd, []string{"test-query"}, port)

	_ = w.Close()
	os.Stdout = oldStdout
	output, _ := io.ReadAll(r)

	assert.NoError(t, resultErr)
	assert.Contains(t, string(output), "nodes")
}

// TestRouteStandardCommand_TreeWithServer_RoutesCorrectly verifies tree routing.
func TestRouteStandardCommand_TreeWithServer_RoutesCorrectly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/nodes/PROJ-1/tree", r.URL.Path)

		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"PROJ-1","children":[]}`))
	}))
	defer srv.Close()

	parts := strings.Split(srv.URL, ":")
	portStr := parts[len(parts)-1]
	var port int
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}

	cmd := newTreeCmd()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	resultErr := routeStandardCommand(cmd, []string{"PROJ-1"}, port)

	_ = w.Close()
	os.Stdout = oldStdout
	output, _ := io.ReadAll(r)

	assert.NoError(t, resultErr)
	assert.Contains(t, string(output), "PROJ-1")
}

// TestReadPIDLock_OnlyPIDNoPort_ReturnsNotAlive verifies incomplete lock file.
func TestReadPIDLock_OnlyPIDNoPort_ReturnsNotAlive(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, pidLockFile)

	require.NoError(t, os.WriteFile(lockPath, []byte("12345\n"), 0o600))

	port, alive := readPIDLock(tmpDir)
	assert.Equal(t, 0, port)
	assert.False(t, alive)
}

// TestReadPIDLock_InvalidPort_ReturnsNotAlive verifies bad port in lock.
func TestReadPIDLock_InvalidPort_ReturnsNotAlive(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, pidLockFile)

	require.NoError(t, os.WriteFile(lockPath, []byte("12345\nnot-a-port\n"), 0o600))

	port, alive := readPIDLock(tmpDir)
	assert.Equal(t, 0, port)
	assert.False(t, alive)
}

// TestReadPIDLock_EmptyFile_ReturnsNotAlive verifies empty lock file.
func TestReadPIDLock_EmptyFile_ReturnsNotAlive(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, pidLockFile)

	require.NoError(t, os.WriteFile(lockPath, []byte(""), 0o600))

	port, alive := readPIDLock(tmpDir)
	assert.Equal(t, 0, port)
	assert.False(t, alive)
}
