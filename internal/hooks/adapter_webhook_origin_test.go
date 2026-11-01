// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func listSource(origins ...Origin) OriginSource {
	return func() (OriginList, error) { return OriginList{Origins: origins}, nil }
}

func originOf(t *testing.T, raw string, private bool) Origin {
	t.Helper()
	o, err := NormalizeOrigin(raw)
	require.NoError(t, err)
	return Origin{Origin: o, AllowPrivate: private}
}

func newTestAdapter(src OriginSource) *WebhookAdapter {
	a := NewWebhookAdapter(src)
	a.backoff = time.Millisecond
	return a
}

func TestWebhook_UnconsentedOrigin_RefusedWithoutRequest(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	a := newTestAdapter(listSource())
	err := a.Deliver(context.Background(), webhookDelivery(srv.URL, []byte(`{}`)))
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused))
	require.Contains(t, refused.Fix, "mtix hooks allow-origin")
	require.Zero(t, atomic.LoadInt32(&hits))
}

func TestWebhook_UnreadableConsent_FixNamesRecord(t *testing.T) {
	err := newTestAdapter(func() (OriginList, error) { return OriginList{}, errors.New("corrupt") }).
		Deliver(context.Background(), webhookDelivery("https://w.example/h", []byte(`{}`)))
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused))
	require.Contains(t, refused.Fix, webhookOriginsRecord+".json")
}

func TestWebhook_NilOrFailingSource_FailsClosed(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	for name, src := range map[string]OriginSource{
		"nil":   nil,
		"error": func() (OriginList, error) { return OriginList{}, errors.New("unreadable") },
	} {
		t.Run(name, func(t *testing.T) {
			err := newTestAdapter(src).Deliver(context.Background(), webhookDelivery(srv.URL, []byte(`{}`)))
			var refused *OriginRefusedError
			require.True(t, errors.As(err, &refused))
		})
	}
	require.Zero(t, atomic.LoadInt32(&hits))
}

func TestWebhook_LoopbackListedWithoutPrivate_RefusedAtDial(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	a := newTestAdapter(listSource(originOf(t, srv.URL, false)))
	err := a.Deliver(context.Background(), webhookDelivery(srv.URL, []byte(`{}`)))
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused), "consent alone does not open a private address")
	require.Zero(t, atomic.LoadInt32(&hits))
}

func TestWebhook_ListedPrivate_Delivers(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	a := newTestAdapter(listSource(originOf(t, srv.URL, true)))
	require.NoError(t, a.Deliver(context.Background(), webhookDelivery(srv.URL, []byte(`{}`))))
	require.Equal(t, int32(1), atomic.LoadInt32(&hits))
}

func TestWebhook_RebindingName_RefusedOnResolvedAddress(t *testing.T) {
	a := newTestAdapter(listSource(Origin{Origin: "http://rebind.example:80"}))
	a.lookup = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("127.0.0.1")}, nil
	}
	err := a.Deliver(context.Background(), webhookDelivery("http://rebind.example/h", []byte(`{}`)))
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused), "any blocked address in the answer set refuses the name")
}

func TestWebhook_RebindingName_PrivateAnswerRefusedAndNotRetried(t *testing.T) {
	var lookups int32
	a := newTestAdapter(listSource(Origin{Origin: "http://rebind.example:80"}))
	a.lookup = func(context.Context, string) ([]net.IP, error) {
		atomic.AddInt32(&lookups, 1)
		return []net.IP{net.ParseIP("169.254.169.254")}, nil
	}
	err := a.Deliver(context.Background(), webhookDelivery("http://rebind.example/h", []byte(`{}`)))
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused))
	require.Equal(t, int32(1), atomic.LoadInt32(&lookups), "a refusal is final, never retried")
}

func TestWebhook_RedirectToUnlistedOrigin_NotFollowed(t *testing.T) {
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&targetHits, 1) }))
	defer target.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	a := newTestAdapter(listSource(originOf(t, first.URL, true)))
	err := a.Deliver(context.Background(), webhookDelivery(first.URL, []byte(`{}`)))
	require.Error(t, err)
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused))
	require.Contains(t, refused.Reason, "redirect to an origin that is not consented", "refused by the redirect guard, not only the dial guard")
	require.Zero(t, atomic.LoadInt32(&targetHits))
}

func TestWebhook_RedirectToUnlistedButDialableOrigin_NotFollowed(t *testing.T) {
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&targetHits, 1) }))
	defer target.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	// The target's host:port has the private opt-in under the other scheme, so
	// the dial guard would let the connection through; only the redirect guard
	// stands between the POST and the unlisted http origin.
	targetOther := originOf(t, "https://"+strings.TrimPrefix(target.URL, "http://"), true)
	a := newTestAdapter(listSource(originOf(t, first.URL, true), targetOther))
	err := a.Deliver(context.Background(), webhookDelivery(first.URL, []byte(`{}`)))
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused))
	require.Contains(t, refused.Reason, "redirect to an origin that is not consented")
	require.Zero(t, atomic.LoadInt32(&targetHits))
}

func TestWebhook_RedirectToListedOrigin_Followed(t *testing.T) {
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&targetHits, 1) }))
	defer target.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	a := newTestAdapter(listSource(originOf(t, first.URL, true), originOf(t, target.URL, true)))
	require.NoError(t, a.Deliver(context.Background(), webhookDelivery(first.URL, []byte(`{}`))))
	require.Equal(t, int32(1), atomic.LoadInt32(&targetHits))
}

func TestWebhook_RedirectToListedButPrivateNotAllowed_Refused(t *testing.T) {
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&targetHits, 1) }))
	defer target.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	a := newTestAdapter(listSource(originOf(t, first.URL, true), originOf(t, target.URL, false)))
	err := a.Deliver(context.Background(), webhookDelivery(first.URL, []byte(`{}`)))
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused))
	require.Zero(t, atomic.LoadInt32(&targetHits))
}

func TestWebhook_GuardedClient_NeverUsesAProxy(t *testing.T) {
	tr, ok := newTestAdapter(listSource()).guardedClient(OriginList{}).Transport.(schemeTransport).base.(*http.Transport)
	require.True(t, ok)
	require.Nil(t, tr.Proxy, "a proxy would move the connection outside the dial check")
}

func TestWebhook_EnvironmentProxy_Ignored(t *testing.T) {
	var proxyHits int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&proxyHits, 1) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	a := newTestAdapter(listSource(originOf(t, srv.URL, true)))
	require.NoError(t, a.Deliver(context.Background(), webhookDelivery(srv.URL, []byte(`{}`))))
	require.Equal(t, int32(1), atomic.LoadInt32(&hits))
	require.Zero(t, atomic.LoadInt32(&proxyHits), "a proxy would bypass the dialed-address check")
}

func TestDialVetted_BlockedAddressWithoutPreCheck_RefusedByControlHook(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	_, err = dialVetted(context.Background(), "tcp", []net.IP{net.ParseIP("127.0.0.1")}, port, dialPolicy{})
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused), "the socket-level check holds even if the answer pre-check is bypassed")
	conn, err := dialVetted(context.Background(), "tcp", []net.IP{net.ParseIP("127.0.0.1")}, port, dialPolicy{allowPrivate: true})
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func TestGuardedDial_BlockedSecondAnswer_RefusedBeforeAnyDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a dial of any answer would fail with the context error, not a refusal
	dial := guardedDialContext(OriginList{}, func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("127.0.0.1")}, nil
	})
	_, err := dial(ctx, "tcp", "mixed.example:80")
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused), "every answer is vetted before the first is dialed")
}
