// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Load reads and validates .mtix/hooks.yaml from mtixDir. It NEVER fails the
// caller (FR-19 NFR: "a bad hook config disables THAT hook with a warning,
// never the CLI"): a missing file yields an empty Config; a malformed file or
// an invalid hook is reported via the returned warnings and dropped, leaving
// the valid hooks active. Callers log the warnings and carry on.
func Load(mtixDir string) (Config, []string) {
	path := filepath.Join(mtixDir, "hooks.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil // no hooks configured — the common case
		}
		return Config{}, []string{fmt.Sprintf("hooks.yaml: read failed, hooks disabled: %v", err)}
	}
	var raw Config
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, []string{fmt.Sprintf("hooks.yaml: parse failed, hooks disabled: %v", err)}
	}
	return validate(raw)
}

// validate drops each invalid hook (returning a warning naming it) and keeps
// the rest — one bad hook never disables the others.
func validate(cfg Config) (Config, []string) {
	var (
		out      Config
		warnings []string
		seen     = map[string]bool{}
	)
	for i, h := range cfg.Hooks {
		if problem := h.problem(seen); problem != "" {
			label := h.Name
			if label == "" {
				label = fmt.Sprintf("#%d", i)
			}
			warnings = append(warnings, fmt.Sprintf("hook %q disabled: %s", label, problem))
			continue
		}
		seen[h.Name] = true
		out.Hooks = append(out.Hooks, h)
	}
	return out, warnings
}

// problem returns "" when the hook is well-formed, otherwise the reason it is
// being disabled. Split into focused checks to keep each simple.
func (h Hook) problem(seen map[string]bool) string {
	if s := h.basicProblem(seen); s != "" {
		return s
	}
	if s := h.eventProblem(); s != "" {
		return s
	}
	return h.deliverProblem()
}

func (h Hook) basicProblem(seen map[string]bool) string {
	switch {
	case h.Name == "":
		return "missing name"
	case seen[h.Name]:
		return "duplicate name"
	case len(h.Match.Events) == 0:
		return "match.events is required (subscribe to at least one event)"
	case len(h.Deliver) == 0:
		return "deliver is required (name at least one adapter)"
	default:
		return ""
	}
}

func (h Hook) eventProblem() string {
	for _, ev := range h.Match.Events {
		if !isKnownEvent(ev) {
			return fmt.Sprintf("unknown event %q", ev)
		}
	}
	return ""
}

func (h Hook) deliverProblem() string {
	for _, d := range h.Deliver {
		switch d {
		case AdapterInbox, AdapterExec, AdapterWebhook, AdapterAppendFile:
		default:
			return fmt.Sprintf("unknown deliver adapter %q", d)
		}
	}
	switch {
	case contains(h.Deliver, AdapterExec) && (h.Exec == nil || len(h.Exec.Command) == 0):
		return "exec adapter requires exec.command (argv)"
	case contains(h.Deliver, AdapterWebhook) && (h.Webhook == nil || h.Webhook.URL == ""):
		return "webhook adapter requires webhook.url"
	case contains(h.Deliver, AdapterAppendFile) && (h.AppendFile == nil || h.AppendFile.Path == ""):
		return "append-file adapter requires append-file.path"
	default:
		return ""
	}
}

func isKnownEvent(ev string) bool {
	switch ev {
	case EventCommentAddressed, EventStatusChanged, EventNodeCreated:
		return true
	default:
		return false
	}
}
