// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

// webhookOriginsRecord names the operator-local consent record (FR-19, W15).
// It lives in the host operator directory, never in the project tree, so a
// cloned repository cannot grant itself delivery rights.
const webhookOriginsRecord = "webhook-origins"

// maxConsentedOrigins bounds the consent list so the record stays small.
const maxConsentedOrigins = 256

// Origin is one operator-consented delivery origin ("scheme://host:port").
// AllowPrivate is the separate, explicit opt-in for loopback, link-local and
// private destinations.
type Origin struct {
	Origin       string `json:"origin"`
	AllowPrivate bool   `json:"allow_private,omitempty"`
}

// OriginList is the operator-local set of consented origins.
type OriginList struct {
	Origins []Origin `json:"origins"`
}

// OriginSource supplies the consent list at delivery time. An error denies.
type OriginSource func() (OriginList, error)

// OriginRefusedError reports a delivery that was not attempted (or was cut
// off) by origin consent. It is final: callers must not retry it.
type OriginRefusedError struct {
	Origin string
	Reason string
	Fix    string
}

// Error implements error.
func (e *OriginRefusedError) Error() string {
	return fmt.Sprintf("webhook origin %s refused: %s (fix: %s)", e.Origin, e.Reason, e.Fix)
}

// originResetFix is the recovery step when the consent record cannot be read:
// remove the named record (the operator's own file, outside the project), then
// consent again. It names the file when the operator directory resolves.
func originResetFix() string {
	where := "the operator-local mtix directory"
	if env, err := oplocal.FromProcess(); err == nil {
		if dir, err := oplocal.New(env).Path(); err == nil {
			where = dir
		}
	}
	return "remove " + filepath.Join(where, webhookOriginsRecord+".json") +
		", then run mtix hooks allow-origin '<url>' again for each webhook origin (mtix hooks status --json lists them)"
}

// OriginAllowFix returns the exact operator command that consents to origin.
func OriginAllowFix(origin string, private bool) string {
	cmd := "mtix hooks allow-origin"
	if private {
		cmd += " --allow-private"
	}
	return cmd + " '" + strings.ReplaceAll(origin, "'", "'\"'\"'") + "'"
}

// NormalizeOrigin reduces a URL (or origin) to its canonical
// "scheme://host:port" form: http or https only, lower case, no userinfo,
// no trailing dot, explicit port. It is idempotent: an input whose result
// would not normalize to itself (a zoned IPv6 literal, a doubled trailing
// dot) is refused, so anything it returns can be stored and read back.
func NormalizeOrigin(raw string) (string, error) {
	origin, err := normalizeOnce(raw)
	if err != nil {
		return "", err
	}
	again, err := normalizeOnce(origin)
	if err != nil || again != origin {
		return "", errors.New("invalid webhook URL: host is not in canonical form")
	}
	return origin, nil
}

func normalizeOnce(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid webhook URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("invalid webhook URL: scheme must be http or https")
	}
	if u.User != nil {
		return "", errors.New("invalid webhook URL: credentials in the URL are not allowed")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || strings.Contains(host, "%") {
		return "", errors.New("invalid webhook URL: host is empty or carries a zone")
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[scheme]
	}
	return scheme + "://" + net.JoinHostPort(host, port), nil
}

// Find returns the consent entry for a normalized origin.
func (l OriginList) Find(origin string) (Origin, bool) {
	for _, o := range l.Origins {
		if o.Origin == origin {
			return o, true
		}
	}
	return Origin{}, false
}

// ReadOrigins loads the consent list. A missing record is an empty list; an
// unreadable, corrupt, foreign-host or invalid record is an error, which every
// caller treats as a denial.
func ReadOrigins(state *oplocal.State) (OriginList, error) {
	if state == nil {
		return OriginList{}, errors.New("operator state unavailable")
	}
	var list OriginList
	reports, err := state.ReadJSON(webhookOriginsRecord, &list)
	if err != nil {
		return OriginList{}, fmt.Errorf("read webhook origins: %w", err)
	}
	if len(reports) > 0 {
		return OriginList{}, fmt.Errorf("webhook origins ignored: %s", reports[0].Reason)
	}
	if err := validateOrigins(list); err != nil {
		return OriginList{}, err
	}
	return list, nil
}

// validateOrigins rejects a record that is over the cap or holds an entry
// that is not in canonical form, so a damaged record denies instead of
// silently matching nothing.
func validateOrigins(list OriginList) error {
	if len(list.Origins) > maxConsentedOrigins {
		return fmt.Errorf("webhook origins record has %d entries (limit %d)", len(list.Origins), maxConsentedOrigins)
	}
	for _, o := range list.Origins {
		if canonical, err := NormalizeOrigin(o.Origin); err != nil || canonical != o.Origin {
			return fmt.Errorf("webhook origins record has a malformed entry %q", o.Origin)
		}
	}
	return nil
}

// StateOriginSource reads the consent list from operator state on each call.
func StateOriginSource(state *oplocal.State) OriginSource {
	return func() (OriginList, error) { return ReadOrigins(state) }
}

// ProcessOriginSource reads the consent list from the operator state of the
// current process. Environment errors deny, like an unreadable record.
func ProcessOriginSource() OriginSource {
	return func() (OriginList, error) {
		env, err := oplocal.FromProcess()
		if err != nil {
			return OriginList{}, fmt.Errorf("operator inputs: %w", err)
		}
		return ReadOrigins(oplocal.New(env))
	}
}

// AllowOrigin records operator consent for the origin of rawURL (W15, TM I18).
// It requires terminal stdin and no agent session marker, and rewrites the
// record atomically with owner-only permissions. Re-listing an origin updates
// its private flag.
func AllowOrigin(state *oplocal.State, rawURL string, allowPrivate, terminal, session bool) (Origin, error) {
	if !terminal || session {
		return Origin{}, errors.New("origin consent requires terminal stdin outside an agent session")
	}
	origin, err := NormalizeOrigin(rawURL)
	if err != nil {
		return Origin{}, err
	}
	list, err := ReadOrigins(state)
	if err != nil {
		return Origin{}, err
	}
	entry := Origin{Origin: origin, AllowPrivate: allowPrivate}
	if i := slices.IndexFunc(list.Origins, func(o Origin) bool { return o.Origin == origin }); i >= 0 {
		list.Origins[i] = entry
	} else if len(list.Origins) >= maxConsentedOrigins {
		return Origin{}, fmt.Errorf("origin consent list is full (%d entries)", maxConsentedOrigins)
	} else {
		list.Origins = append(list.Origins, entry)
	}
	if err := state.WriteJSON(webhookOriginsRecord, list); err != nil {
		return Origin{}, fmt.Errorf("save webhook origins: %w", err)
	}
	return entry, nil
}

// OriginIssue is one webhook hook whose delivery would be refused, with the
// exact corrective command (hooks status, TM I35).
type OriginIssue struct {
	Hook   string `json:"hook"`
	Origin string `json:"origin,omitempty"`
	Reason string `json:"reason"`
	Fix    string `json:"fix"`
}

// WebhookOriginIssues lists configured webhooks that consent would refuse.
// Each consented hostname is resolved once (bounded, no request is sent to the
// webhook) so a name that resolves to a private address without the opt-in is
// reported with the exact fix; delivery re-checks the address it dials.
func WebhookOriginIssues(list OriginList, listErr error, cfg Config) []OriginIssue {
	return webhookOriginIssues(list, listErr, cfg, statusResolver{lookup: defaultLookup, timeout: statusLookupTimeout})
}

func webhookOriginIssues(list OriginList, listErr error, cfg Config, res statusResolver) []OriginIssue {
	issues := []OriginIssue{}
	for _, h := range cfg.Hooks {
		if h.Webhook == nil || !slices.Contains(h.Deliver, AdapterWebhook) {
			continue
		}
		if issue, bad := webhookOriginIssue(list, listErr, h, res); bad {
			issues = append(issues, issue)
		}
	}
	return issues
}

func webhookOriginIssue(list OriginList, listErr error, h Hook, res statusResolver) (OriginIssue, bool) {
	origin, err := NormalizeOrigin(h.Webhook.URL)
	if err != nil {
		return OriginIssue{Hook: h.Name, Reason: "invalid webhook URL: " + err.Error(), Fix: "edit .mtix/hooks.yaml, then run mtix hooks status --json"}, true
	}
	if listErr != nil {
		return OriginIssue{Hook: h.Name, Origin: origin, Reason: "origin consent state unreadable: " + listErr.Error(), Fix: originResetFix()}, true
	}
	entry, ok := list.Find(origin)
	if !ok {
		return OriginIssue{Hook: h.Name, Origin: origin, Reason: "origin not consented on this host", Fix: OriginAllowFix(origin, privateHost(hostOf(origin)))}, true
	}
	if privateHost(hostOf(origin)) && !entry.AllowPrivate {
		return OriginIssue{Hook: h.Name, Origin: origin, Reason: "private, loopback or link-local address not explicitly allowed", Fix: OriginAllowFix(origin, true)}, true
	}
	if !entry.AllowPrivate {
		return resolutionIssue(h.Name, origin, res)
	}
	return OriginIssue{}, false
}

// statusLookupTimeout bounds the name lookup status and doctor make per
// configured webhook host. No request is sent to the webhook.
const statusLookupTimeout = 2 * time.Second

// statusResolver is the bounded name lookup status and doctor use.
type statusResolver struct {
	lookup  func(context.Context, string) ([]net.IP, error)
	timeout time.Duration
}

// resolve looks host up within the bound even when the lookup ignores its
// context: it returns when the answer arrives or the bound passes.
func (r statusResolver) resolve(host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	type answer struct {
		ips []net.IP
		err error
	}
	done := make(chan answer, 1)
	go func() {
		ips, err := r.lookup(ctx, host)
		done <- answer{ips, err}
	}()
	select {
	case a := <-done:
		return a.ips, a.err
	case <-ctx.Done():
		return nil, fmt.Errorf("lookup of %s timed out after %s", host, r.timeout)
	}
}

// resolutionIssue judges a consented hostname that has no private opt-in: any
// blocked answer is a finding, and a name that cannot be resolved is reported
// as unverified rather than passed. IP literals are judged without DNS.
func resolutionIssue(hook, origin string, res statusResolver) (OriginIssue, bool) {
	host := hostOf(origin)
	if host == "" || net.ParseIP(host) != nil {
		return OriginIssue{}, false
	}
	ips, err := res.resolve(host)
	if err != nil {
		return OriginIssue{
			Hook: hook, Origin: origin,
			Reason: "could not verify where this name resolves (" + err.Error() + "); delivery re-checks the address at send time",
			Fix:    "re-run mtix hooks status --json (or mtix doctor) when online, or check the host resolver",
		}, true
	}
	if slices.ContainsFunc(ips, blockedIP) {
		return OriginIssue{Hook: hook, Origin: origin, Reason: "name resolves to a private, loopback or link-local address; delivery is refused unless the origin is listed with --allow-private", Fix: OriginAllowFix(origin, true)}, true
	}
	return OriginIssue{}, false
}

// privateHost reports whether host is a blocked IP literal or a localhost
// name, which always resolves to loopback. Other names are judged at dial time.
func privateHost(host string) bool {
	h := strings.ToLower(host)
	return IsBlockedAddress(h) || h == "localhost" || strings.HasSuffix(h, ".localhost")
}

func hostOf(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
