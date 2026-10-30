// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
)

// maxWebhookRedirects bounds redirect chains between consented origins.
const maxWebhookRedirects = 3

// blockedPrefixes lists destinations a webhook may reach only when the
// operator listed the origin with --allow-private. Loopback, link-local,
// private and unspecified ranges come from the net package helpers; these add
// shared, benchmarking, reserved and translation ranges that can embed or
// reach internal addresses.
var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4",
	"64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "100::/64",
	"::/96", "fec0::/10", "2001::/32", "::ffff:0:0:0/96", "192.88.99.0/24",
)

func mustPrefixes(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("webhook guard: bad CIDR " + c) // constant input; cannot happen
		}
		out = append(out, n)
	}
	return out
}

// IsBlockedAddress reports whether host (an IP literal) is loopback,
// link-local, private, unspecified, multicast or in a reserved range.
// Anything that does not parse as an IP is not an address and returns false.
func IsBlockedAddress(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return blockedIP(ip)
}

func blockedIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, n := range blockedPrefixes {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// dialPolicy carries what the dial-time checks need to know about the origin
// being contacted: its normalized form (named in refusals) and whether the
// operator listed it with --allow-private.
type dialPolicy struct {
	origin       string
	allowPrivate bool
}

// checkDialAddress validates an address with no known origin; refusals carry
// the address and a generic fix. Delivery uses checkDialAddressFor.
func checkDialAddress(address string, allowPrivate bool) error {
	return checkDialAddressFor(address, dialPolicy{allowPrivate: allowPrivate})
}

// checkDialAddressFor validates the address a socket is about to connect to.
// It runs on the address actually dialed (net.Dialer Control), so DNS
// answers that change between check and connect cannot reach a blocked
// destination. An unparseable address fails closed. A refusal names the
// configured origin and the exact command that would allow it.
func checkDialAddressFor(address string, pol dialPolicy) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return &OriginRefusedError{Origin: address, Reason: "unparseable dial address", Fix: "mtix hooks status --json"}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return &OriginRefusedError{Origin: address, Reason: "dial address is not an IP", Fix: "mtix hooks status --json"}
	}
	if blockedIP(ip) && !pol.allowPrivate {
		origin := pol.origin
		fix := "if intended, list the origin with: mtix hooks allow-origin --allow-private '<origin>'"
		if origin == "" {
			origin = address
		} else {
			fix = OriginAllowFix(origin, true)
		}
		return &OriginRefusedError{
			Origin: origin,
			Reason: "destination resolves to a loopback, link-local or private address",
			Fix:    fix,
		}
	}
	return nil
}

// schemeKey carries the request scheme from the transport to the dial guard,
// which sees only host:port.
type schemeKey struct{}

// schemeTransport records each request's scheme in its context, so the dial
// guard can match the exact consented origin (scheme included), redirects too.
type schemeTransport struct{ base http.RoundTripper }

func (t schemeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := context.WithValue(req.Context(), schemeKey{}, req.URL.Scheme)
	return t.base.RoundTrip(req.WithContext(ctx))
}

// guardedClient builds a client whose every connection (including redirects)
// is checked against the consent list and the actual dialed address. It
// ignores proxy environment variables, which would move the connection
// outside the check.
func (a *WebhookAdapter) guardedClient(list OriginList) *http.Client {
	lookup := a.lookup
	if lookup == nil {
		lookup = defaultLookup
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           guardedDialContext(list, lookup),
		TLSHandshakeTimeout:   webhookTimeout,
		ResponseHeaderTimeout: webhookTimeout,
		DisableKeepAlives:     true,
	}
	return &http.Client{
		Timeout:       webhookTimeout,
		Transport:     schemeTransport{base: tr},
		CheckRedirect: redirectGuard(list),
	}
}

func defaultLookup(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// redirectGuard follows a redirect only to another consented origin; the
// dial guard then re-checks the address it resolves to.
func redirectGuard(list OriginList) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxWebhookRedirects {
			return fmt.Errorf("webhook: stopped after %d redirects", maxWebhookRedirects)
		}
		origin, err := NormalizeOrigin(req.URL.String())
		if err != nil {
			return &OriginRefusedError{Origin: req.URL.Redacted(), Reason: "redirect target is not a valid origin", Fix: "mtix hooks status --json"}
		}
		if _, ok := list.Find(origin); !ok {
			return &OriginRefusedError{Origin: origin, Reason: "redirect to an origin that is not consented", Fix: OriginAllowFix(origin, false)}
		}
		return nil
	}
}

// guardedDialContext resolves the host itself, refuses the whole name when
// any answer is blocked (unless the origin opted into private destinations),
// then dials the vetted IP literal. The dialer's Control hook re-checks the
// address of the socket actually opened.
func guardedDialContext(list OriginList, lookup func(context.Context, string) ([]net.IP, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, &OriginRefusedError{Origin: addr, Reason: "unparseable dial address", Fix: "mtix hooks status --json"}
		}
		pol := dialPolicyFor(ctx, list, host, port)
		ips, err := resolveForDial(ctx, host, lookup)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if err := checkDialAddressFor(net.JoinHostPort(ip.String(), port), pol); err != nil {
				return nil, err
			}
		}
		return dialVetted(ctx, network, ips, port, pol)
	}
}

func resolveForDial(ctx context.Context, host string, lookup func(context.Context, string) ([]net.IP, error)) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve %s: no addresses", host)
	}
	return ips, nil
}

func dialVetted(ctx context.Context, network string, ips []net.IP, port string, pol dialPolicy) (net.Conn, error) {
	d := net.Dialer{
		Timeout: webhookTimeout,
		Control: func(_, address string, _ syscall.RawConn) error { return checkDialAddressFor(address, pol) },
	}
	var lastErr error
	for _, ip := range ips {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		var refused *OriginRefusedError
		if errors.As(err, &refused) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// dialPolicyFor builds the policy for a dial to host:port from the scheme the
// transport recorded. Without a scheme nothing is allowed private.
func dialPolicyFor(ctx context.Context, list OriginList, host, port string) dialPolicy {
	scheme, _ := ctx.Value(schemeKey{}).(string)
	if scheme == "" {
		return dialPolicy{origin: net.JoinHostPort(host, port)}
	}
	raw := scheme + "://" + net.JoinHostPort(host, port)
	origin, err := NormalizeOrigin(raw)
	if err != nil {
		return dialPolicy{origin: raw}
	}
	entry, ok := list.Find(origin)
	return dialPolicy{origin: origin, allowPrivate: ok && entry.AllowPrivate}
}
