// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store"
)

const authTestToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestCheckExposure_BindFlagAndToken_RefusesNetworkBindWithoutBoth is the
// bind x --insecure-bind x token table of MTIX-220: a loopback bind needs
// neither; any other bind needs both, and the refusal names both and the
// exact fix.
func TestCheckExposure_BindFlagAndToken_RefusesNetworkBindWithoutBoth(t *testing.T) {
	binds := []struct {
		bind     string
		loopback bool
	}{
		{"127.0.0.1", true}, {"::1", true}, {"localhost", true}, {"LOCALHOST", true}, {"127.0.0.2", true},
		{"0.0.0.0", false}, {"::", false}, {"192.0.2.10", false}, {"mtix.example.net", false}, {"fd00::10", false},
	}
	for _, b := range binds {
		for _, insecure := range []bool{false, true} {
			for _, token := range []bool{false, true} {
				name := b.bind + " insecure=" + boolName(insecure) + " token=" + boolName(token)
				t.Run(name, func(t *testing.T) {
					err := CheckExposure(b.bind, insecure, token)
					if b.loopback || (insecure && token) {
						require.NoError(t, err)
						return
					}
					require.Error(t, err)
					msg := err.Error()
					assert.Contains(t, msg, "--insecure-bind")
					assert.Contains(t, msg, "token")
					assert.Contains(t, msg, "mtix serve --addr "+b.bind+" --insecure-bind", "the exact fix")
				})
			}
		}
	}
}

func boolName(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// TestServer_Start_RefusesNetworkBindWithoutInsecureBindAndToken pins the
// defence in depth: a library caller that skips the CLI check is refused by
// Start itself, before anything listens.
func TestServer_Start_RefusesNetworkBindWithoutInsecureBindAndToken(t *testing.T) {
	cases := []struct {
		name string
		cfg  ServerConfig
	}{
		{"neither", ServerConfig{Bind: "0.0.0.0", Port: "-1"}},
		{"flag only", ServerConfig{Bind: "0.0.0.0", Port: "-1", InsecureBind: true}},
		{"token only", ServerConfig{Bind: "0.0.0.0", Port: "-1", AuthToken: authTestToken}},
	}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServerWith(t, logger, tc.cfg)
			// Port -1 cannot listen, so a Start that lost the check still
			// returns (with a listen error) instead of blocking the package.
			err := s.Start()
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrExposureRefused)
			assert.Contains(t, err.Error(), "--insecure-bind")
		})
	}
}

func authRequest(method, path, host, authorization string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(`{"title":"Auth check","project":"TEST"}`))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return req
}

// TestServer_NetworkBind_RejectsBadCredentialsOnEveryRoute pins story 2:
// every route class answers 401 AUTH_REQUIRED without a valid bearer token,
// leaks nothing about which part failed and never echoes the token, and a
// refused write creates no node.
func TestServer_NetworkBind_RejectsBadCredentialsOnEveryRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	s := testServerWith(t, logger, ServerConfig{Bind: "192.0.2.10", Port: "0", InsecureBind: true, AuthToken: authTestToken})
	routes := []struct{ method, path string }{
		{http.MethodGet, "/health"}, {http.MethodGet, "/api/openapi.json"}, {http.MethodGet, "/api/openapi.yaml"},
		{http.MethodGet, "/api/v1/nodes"}, {http.MethodPost, "/api/v1/nodes"}, {http.MethodGet, "/ws/events"},
		{http.MethodGet, "/"}, {http.MethodGet, "/no/such/route"},
	}
	bad := map[string]string{
		"missing": "", "empty bearer": "Bearer ", "bare scheme": "Bearer", "basic scheme": "Basic " + authTestToken,
		"wrong token": "Bearer wrong", "token plus suffix": "Bearer " + authTestToken + "x",
		"truncated token": "Bearer " + authTestToken[:len(authTestToken)-1], "token only": authTestToken,
		"double space": "Bearer  " + authTestToken,
	}
	for _, rt := range routes {
		for name, cred := range bad {
			t.Run(rt.method+" "+rt.path+" "+name, func(t *testing.T) {
				w := httptest.NewRecorder()
				s.Router().ServeHTTP(w, authRequest(rt.method, rt.path, "192.0.2.10:8377", cred))
				assert.Equal(t, http.StatusUnauthorized, w.Code)
				assert.Contains(t, w.Body.String(), "AUTH_REQUIRED")
				assert.Equal(t, "Bearer", w.Header().Get("WWW-Authenticate"))
				assert.NotContains(t, w.Body.String(), authTestToken)
			})
		}
		t.Run(rt.method+" "+rt.path+" valid token", func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, authRequest(rt.method, rt.path, "192.0.2.10:8377", "Bearer "+authTestToken))
			assert.NotEqual(t, http.StatusUnauthorized, w.Code)
		})
		t.Run(rt.method+" "+rt.path+" scheme is case-insensitive", func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Router().ServeHTTP(w, authRequest(rt.method, rt.path, "192.0.2.10:8377", "bearer "+authTestToken))
			assert.NotEqual(t, http.StatusUnauthorized, w.Code)
		})
	}
	_, nodes, err := s.store.ListNodes(context.Background(), store.NodeFilter{}, store.ListOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, nodes, "only the two authenticated POSTs (valid token, lower-case scheme) created a node; refused POSTs created none")
}

// TestServer_NetworkBind_AuthRunsBeforeHostOriginAndCSRF pins the order: an
// unauthenticated caller learns nothing else, whatever Host, Origin or CSRF
// header it sends.
func TestServer_NetworkBind_AuthRunsBeforeHostOriginAndCSRF(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	s := testServerWith(t, logger, ServerConfig{Bind: "192.0.2.10", Port: "0", InsecureBind: true, AuthToken: authTestToken})

	badHost := httptest.NewRecorder()
	s.Router().ServeHTTP(badHost, authRequest(http.MethodGet, "/health", "evil.example", ""))
	assert.Equal(t, http.StatusUnauthorized, badHost.Code, "no Host disclosure before auth")

	badOrigin := authRequest(http.MethodGet, "/health", "192.0.2.10:8377", "")
	badOrigin.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, badOrigin)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "no Origin disclosure before auth")

	noCSRF := authRequest(http.MethodPost, "/api/v1/nodes", "192.0.2.10:8377", "")
	noCSRF.Header.Del("X-Requested-With")
	w = httptest.NewRecorder()
	s.Router().ServeHTTP(w, noCSRF)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "no CSRF disclosure before auth")

	// After auth the existing checks still apply.
	authed := authRequest(http.MethodGet, "/health", "evil.example", "Bearer "+authTestToken)
	w = httptest.NewRecorder()
	s.Router().ServeHTTP(w, authed)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "HOST_NOT_ALLOWED")
	authedNoCSRF := authRequest(http.MethodPost, "/api/v1/nodes", "192.0.2.10:8377", "Bearer "+authTestToken)
	authedNoCSRF.Header.Del("X-Requested-With")
	w = httptest.NewRecorder()
	s.Router().ServeHTTP(w, authedNoCSRF)
	assert.Equal(t, http.StatusForbidden, w.Code, "CSRF still applies after auth")
}

// TestServer_NetworkBind_PreflightNeedsNoTokenButCarriesNoData pins the one
// pass-through: a CORS preflight from an allowed origin gets 204 and no
// data; a disallowed origin is refused.
func TestServer_NetworkBind_PreflightNeedsNoTokenButCarriesNoData(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	s := testServerWith(t, logger, ServerConfig{Bind: "192.0.2.10", Port: "8377", InsecureBind: true, AuthToken: authTestToken})

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/nodes", nil)
	req.Host = "192.0.2.10:8377"
	req.Header.Set("Origin", "http://192.0.2.10:8377")
	w := httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.String())
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Headers"), "Authorization")

	req = httptest.NewRequest(http.MethodOptions, "/api/v1/nodes", nil)
	req.Host = "192.0.2.10:8377"
	req.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	s.Router().ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestServer_Loopback_NeedsNoToken pins that loopback behaviour is unchanged:
// no credential is requested, even when a token value is configured.
func TestServer_Loopback_NeedsNoToken(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	for _, cfg := range []ServerConfig{
		{Bind: "127.0.0.1", Port: "0"},
		{Bind: "127.0.0.1", Port: "0", AuthToken: authTestToken},
		{Bind: "::1", Port: "0"},
	} {
		s := testServerWith(t, logger, cfg)
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, authRequest(http.MethodGet, "/health", "127.0.0.1:8377", ""))
		assert.Equal(t, http.StatusOK, w.Code, cfg.Bind)
	}
}

// TestServer_NetworkBind_NeverLogsTheToken pins that neither the configured
// token nor a presented wrong one reaches the log.
func TestServer_NetworkBind_NeverLogsTheToken(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := testServerWith(t, logger, ServerConfig{Bind: "192.0.2.10", Port: "0", InsecureBind: true, AuthToken: authTestToken})
	for _, cred := range []string{"", "Bearer secret-guess-1234", "Bearer " + authTestToken} {
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, authRequest(http.MethodGet, "/api/v1/nodes", "192.0.2.10:8377", cred))
	}
	assert.NotContains(t, logs.String(), authTestToken)
	assert.NotContains(t, logs.String(), "secret-guess-1234")
}

// TestAuthMiddleware_Source_UsesConstantTimeComparison pins the comparison
// primitive: digests of both sides go through crypto/subtle, never ==.
func TestAuthMiddleware_Source_UsesConstantTimeComparison(t *testing.T) {
	src, err := os.ReadFile("auth.go")
	require.NoError(t, err)
	text := string(src)
	assert.Contains(t, text, "subtle.ConstantTimeCompare")
	assert.Contains(t, text, "sha256.Sum256")
	assert.NotContains(t, text, "bytes.Equal")
	assert.NotContains(t, text, "== s.config.AuthToken")
}

func wsAuthServer(t *testing.T) (*serverFixture, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	s := testServerWith(t, logger, ServerConfig{Bind: "192.0.2.10", Port: "0", InsecureBind: true, AuthToken: authTestToken})
	ts := httptest.NewServer(s.Router())
	t.Cleanup(func() {
		closeWSTestHub(s.wsHub)
		ts.Close()
	})
	return s, "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events"
}

// TestWebSocket_NetworkBind_AcceptsBearerHeaderOrSubprotocolOnly pins story
// 3: the bearer header and the mtix.auth.TOKEN subprotocol authenticate the
// upgrade, a query string or wrong value does not, and the server answers
// with the fixed subprotocol name, never the token.
func TestWebSocket_NetworkBind_AcceptsBearerHeaderOrSubprotocolOnly(t *testing.T) {
	_, url := wsAuthServer(t)

	header := http.Header{"Authorization": []string{"Bearer " + authTestToken}}
	conn, resp, err := websocket.DefaultDialer.Dial(url, header)
	require.NoError(t, err)
	assert.Equal(t, 101, resp.StatusCode)
	_ = conn.Close()

	dialer := websocket.Dialer{Subprotocols: []string{"mtix.auth", "mtix.auth." + authTestToken}}
	conn, resp, err = dialer.Dial(url, nil)
	require.NoError(t, err)
	assert.Equal(t, "mtix.auth", resp.Header.Get("Sec-WebSocket-Protocol"), "fixed subprotocol only")
	assert.NotContains(t, resp.Header.Get("Sec-WebSocket-Protocol"), authTestToken)
	_ = conn.Close()

	rejected := []struct {
		name   string
		url    string
		dialer websocket.Dialer
		header http.Header
	}{
		{"no credential", url, websocket.Dialer{}, nil},
		{"query string token", url + "?token=" + authTestToken, websocket.Dialer{}, nil},
		{"wrong subprotocol token", url, websocket.Dialer{Subprotocols: []string{"mtix.auth", "mtix.auth.wrong"}}, nil},
		{"fixed subprotocol alone", url, websocket.Dialer{Subprotocols: []string{"mtix.auth"}}, nil},
		{"wrong bearer", url, websocket.Dialer{}, http.Header{"Authorization": []string{"Bearer wrong"}}},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			conn, resp, err := tc.dialer.Dial(tc.url, tc.header)
			if conn != nil {
				_ = conn.Close()
			}
			require.Error(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})
	}
}

// TestWebSocket_NetworkBind_NeverEchoesTheTokenWhateverTheOfferOrder pins the
// never-echo amendment for every offer order: the answered subprotocol is the
// fixed name or none, never the token form.
func TestWebSocket_NetworkBind_NeverEchoesTheTokenWhateverTheOfferOrder(t *testing.T) {
	_, url := wsAuthServer(t)
	offers := [][]string{
		{"mtix.auth", "mtix.auth." + authTestToken},
		{"mtix.auth." + authTestToken, "mtix.auth"},
		{"mtix.auth." + authTestToken},
	}
	for _, offer := range offers {
		dialer := websocket.Dialer{Subprotocols: offer}
		conn, resp, err := dialer.Dial(url, nil)
		require.NoError(t, err, offer)
		answered := resp.Header.Get("Sec-WebSocket-Protocol")
		assert.NotContains(t, answered, authTestToken, offer)
		assert.Contains(t, []string{"", "mtix.auth"}, answered, offer)
		_ = conn.Close()
	}
}

// TestServer_NetworkBind_PreflightBypassKeysOnTheMethodAlone pins that only a
// real OPTIONS request skips auth: a GET or POST that merely claims to be a
// preflight is refused, and an OPTIONS answer carries no data.
func TestServer_NetworkBind_PreflightBypassKeysOnTheMethodAlone(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	s := testServerWith(t, logger, ServerConfig{Bind: "192.0.2.10", Port: "8377", InsecureBind: true, AuthToken: authTestToken})
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := authRequest(method, "/api/v1/nodes", "192.0.2.10:8377", "")
		req.Header.Set("Access-Control-Request-Method", "GET")
		req.Header.Set("Origin", "http://192.0.2.10:8377")
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code, method)
	}
	for _, origin := range []string{"", "http://192.0.2.10:8377"} {
		req := httptest.NewRequest(http.MethodOptions, "/api/v1/nodes", nil)
		req.Host = "192.0.2.10:8377"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, req)
		assert.Equal(t, http.StatusNoContent, w.Code, "origin %q", origin)
		assert.Empty(t, w.Body.String(), "an OPTIONS answer carries no data")
	}
}

// TestServer_NetworkBind_SubprotocolCredentialWorksOnlyOnTheWebSocketRoute pins
// that the mtix.auth.TOKEN subprotocol authenticates /ws/events alone: sent to
// a REST route it is not a credential.
func TestServer_NetworkBind_SubprotocolCredentialWorksOnlyOnTheWebSocketRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	s := testServerWith(t, logger, ServerConfig{Bind: "192.0.2.10", Port: "0", InsecureBind: true, AuthToken: authTestToken})
	for _, path := range []string{"/api/v1/nodes", "/health", "/api/openapi.json", "/"} {
		req := authRequest(http.MethodGet, path, "192.0.2.10:8377", "")
		req.Header.Set("Sec-WebSocket-Protocol", "mtix.auth, mtix.auth."+authTestToken)
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code, path)
	}
}

// TestServer_NetworkBind_NearCollisionTokensAreRefused pins the comparison
// behaviourally: tokens that differ from the real one in a single position are
// refused wherever that position is.
func TestServer_NetworkBind_NearCollisionTokensAreRefused(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	s := testServerWith(t, logger, ServerConfig{Bind: "192.0.2.10", Port: "0", InsecureBind: true, AuthToken: authTestToken})
	for _, pos := range []int{0, 1, len(authTestToken) / 2, len(authTestToken) - 2, len(authTestToken) - 1} {
		near := []byte(authTestToken)
		near[pos] = 'f' + byte(0)
		if near[pos] == authTestToken[pos] {
			near[pos] = 'e'
		}
		w := httptest.NewRecorder()
		s.Router().ServeHTTP(w, authRequest(http.MethodGet, "/health", "192.0.2.10:8377", "Bearer "+string(near)))
		assert.Equal(t, http.StatusUnauthorized, w.Code, "differs at %d", pos)
	}
}
