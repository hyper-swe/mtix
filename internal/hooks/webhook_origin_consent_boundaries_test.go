// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil"
)

func res(l func(context.Context, string) ([]net.IP, error)) statusResolver {
	return statusResolver{lookup: l, timeout: time.Second}
}

func fixedLookup(ip string, calls *int32) func(context.Context, string) ([]net.IP, error) {
	return func(context.Context, string) ([]net.IP, error) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		return []net.IP{net.ParseIP(ip)}, nil
	}
}

// The consent list is enforced on its own: a public, non-consented origin is
// refused by the consent check, before any name is resolved or dialed.
func TestWebhook_PublicUnconsentedOrigin_RefusedByConsentCheckBeforeResolve(t *testing.T) {
	var lookups int32
	a := newTestAdapter(listSource(originOf(t, "http://other.example:80", false)))
	a.lookup = fixedLookup("192.0.2.1", &lookups) // TEST-NET-1: public, not blocked
	err := a.Deliver(context.Background(), webhookDelivery("http://public.example:9/h", []byte(`{}`)))
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused))
	require.Contains(t, refused.Reason, "not consented")
	require.Equal(t, "http://public.example:9", refused.Origin)
	require.Zero(t, atomic.LoadInt32(&lookups), "the consent check runs before any resolution or dial")
}

// A dial-time refusal for a consented name carries the configured origin and
// the exact fix, not the dialed address and a placeholder.
func TestWebhook_ConsentedNameResolvingPrivate_RefusalNamesOriginAndExactFix(t *testing.T) {
	a := newTestAdapter(listSource(originOf(t, "http://hooks.example:8080", false)))
	a.lookup = fixedLookup("127.0.0.1", nil)
	err := a.Deliver(context.Background(), webhookDelivery("http://hooks.example:8080/h", []byte(`{}`)))
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused))
	require.Equal(t, "http://hooks.example:8080", refused.Origin)
	require.Equal(t, OriginAllowFix("http://hooks.example:8080", true), refused.Fix)
	require.NotContains(t, refused.Fix, "<")
}

// The private opt-in is per origin: an opt-in for the http origin does not let
// the https origin of the same host and port dial a private address.
func TestWebhook_PrivateOptInOnOtherScheme_DoesNotApply(t *testing.T) {
	a := newTestAdapter(listSource(
		originOf(t, "http://hooks.example:8443", true),
		originOf(t, "https://hooks.example:8443", false),
	))
	a.lookup = fixedLookup("127.0.0.1", nil)
	err := a.Deliver(context.Background(), webhookDelivery("https://hooks.example:8443/h", []byte(`{}`)))
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused), "https must be refused: %v", err)
	require.Equal(t, "https://hooks.example:8443", refused.Origin)
}

// A redirect chain between consented origins is cut at the cap.
func TestWebhook_RedirectChainBetweenConsentedOrigins_StopsAtCap(t *testing.T) {
	var hits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Redirect(w, r, srv.URL+"/again", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	a := newTestAdapter(listSource(originOf(t, srv.URL, true)))
	err := a.Deliver(context.Background(), webhookDelivery(srv.URL+"/h", []byte(`{}`)))
	require.Error(t, err)
	require.Equal(t, int32((3+1)*3), atomic.LoadInt32(&hits), "3 attempts, each the initial request plus 3 allowed redirects")
}

func TestIsBlockedAddress_TranslatedAndRelayRanges_Refused(t *testing.T) {
	for _, ip := range []string{"2002:7f00:1::1", "::ffff:0:7f00:1", "192.88.99.1"} {
		require.True(t, IsBlockedAddress(ip), ip)
	}
}

// Consent writes fail closed when the existing record cannot be trusted: the
// record bytes are left exactly as they were.
func TestAllowOrigin_UntrustedExistingRecord_FailsClosedAndLeavesBytes(t *testing.T) {
	over := `{"origins":[`
	for i := 0; i <= maxConsentedOrigins; i++ {
		if i > 0 {
			over += ","
		}
		over += `{"origin":"https://h` + strconv.Itoa(i) + `.example:443"}`
	}
	over += `]}`
	tests := []struct{ name, record string }{
		{"corrupt json", "{broken"},
		{"malformed entry", `{"origins":[{"origin":"not an origin"}]}`},
		{"over the cap", over},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := testutil.OperatorState(t)
			_, err := AllowOrigin(state, "https://seed.example", false, true, false)
			require.NoError(t, err)
			dir, err := state.Path()
			require.NoError(t, err)
			file := filepath.Join(dir, webhookOriginsRecord+".json")
			require.NoError(t, os.WriteFile(file, []byte(tt.record), 0o600))

			_, err = AllowOrigin(state, "https://hooks.example", false, true, false)
			require.Error(t, err, "an untrusted record is never overwritten")
			got, readErr := os.ReadFile(file)
			require.NoError(t, readErr)
			require.True(t, bytes.Equal([]byte(tt.record), got), "record bytes unchanged")
		})
	}
}

// Status and doctor resolve configured names: a consented name that resolves
// to a private address without the opt-in is reported with the exact fix.
func TestWebhookOriginIssues_ConsentedNameResolvingPrivate_ReportedWithFix(t *testing.T) {
	cfg := Config{Hooks: []Hook{{Name: "out", Deliver: []string{AdapterWebhook}, Webhook: &WebhookConfig{URL: "https://hooks.example/x"}}}}
	consent := func(private bool) OriginList {
		return OriginList{Origins: []Origin{originOf(t, "https://hooks.example", private)}}
	}
	t.Run("private without opt-in", func(t *testing.T) {
		issues := webhookOriginIssues(consent(false), nil, cfg, res(fixedLookup("10.0.0.5", nil)))
		require.Len(t, issues, 1)
		require.Equal(t, "https://hooks.example:443", issues[0].Origin)
		require.Equal(t, OriginAllowFix("https://hooks.example:443", true), issues[0].Fix)
		require.Contains(t, issues[0].Reason, "resolves")
	})
	t.Run("private with opt-in", func(t *testing.T) {
		require.Empty(t, webhookOriginIssues(consent(true), nil, cfg, res(fixedLookup("10.0.0.5", nil))))
	})
	t.Run("public", func(t *testing.T) {
		require.Empty(t, webhookOriginIssues(consent(false), nil, cfg, res(fixedLookup("192.0.2.1", nil))))
	})
	t.Run("lookup failure is reported as unverified, never passed", func(t *testing.T) {
		fail := func(context.Context, string) ([]net.IP, error) { return nil, errors.New("no resolver") }
		issues := webhookOriginIssues(consent(false), nil, cfg, res(fail))
		require.Len(t, issues, 1)
		require.Contains(t, issues[0].Reason, "could not verify")
		require.Contains(t, issues[0].Reason, "no resolver")
		require.Contains(t, issues[0].Fix, "when online")
		require.Equal(t, "https://hooks.example:443", issues[0].Origin)
	})
	t.Run("every answer is checked, a private second answer warns", func(t *testing.T) {
		two := func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("10.0.0.5")}, nil
		}
		issues := webhookOriginIssues(consent(false), nil, cfg, res(two))
		require.Len(t, issues, 1)
		require.Contains(t, issues[0].Reason, "resolves to a private")
	})
	t.Run("a slow resolver is bounded and reported as unverified", func(t *testing.T) {
		block := make(chan struct{})
		defer close(block)
		slow := func(context.Context, string) ([]net.IP, error) { <-block; return nil, nil } // ignores its context
		start := time.Now()
		issues := webhookOriginIssues(consent(false), nil, cfg, statusResolver{lookup: slow, timeout: 50 * time.Millisecond})
		require.Less(t, time.Since(start), 2*time.Second)
		require.Len(t, issues, 1)
		require.Contains(t, issues[0].Reason, "could not verify")
		require.Contains(t, issues[0].Reason, "timed out")
	})
}
