// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/hyper-swe/mtix/internal/model"
)

func effectiveWakeSettings(raw map[string]json.RawMessage, agent string) (map[string]model.WakeSetting, error) {
	if agent != "" && !validLearnedID(agent) {
		return nil, invalid("wake agent id is invalid")
	}
	out := map[string]model.WakeSetting{}
	for key, value := range model.WakeSettingDefaults() {
		out[key] = model.WakeSetting{Value: value, Source: "default"}
	}
	legacy := map[string]json.RawMessage{}
	for old, key := range map[string]string{"max_events": "wake.envelope_max_events", "max_body_bytes": "wake.envelope_max_body_bytes", "max_total_bytes": "wake.envelope_max_total_bytes", "inbox.strict_board_identity": "inbox.strict_board_identity"} {
		if value, ok := raw[old]; ok {
			legacy[key] = value
		}
	}
	if err := applyWakeValues(out, legacy, "host (legacy)"); err != nil {
		return nil, err
	}
	host, agents, err := wakeScope(raw, "")
	if err != nil {
		return nil, err
	}
	if applyErr := applyWakeValues(out, host, "host"); applyErr != nil {
		return nil, applyErr
	}
	hostOff := out["wake.live"].Value == "false"
	hostLive := out["wake.live"]
	selected, err := selectedAgentWakeValues(agents, agent)
	if err != nil {
		return nil, err
	}
	for key, value := range selected {
		out[key] = value
	}
	if hostOff {
		out["wake.live"] = hostLive
	}
	if _, capsErr := wakeEnvelopeCaps(out); capsErr != nil {
		return nil, capsErr
	}
	return out, nil
}

func applyWakeValues(out map[string]model.WakeSetting, scope map[string]json.RawMessage, source string) error {
	for key, raw := range scope {
		if !model.IsWakeSetting(key) {
			continue
		}
		value, err := wakeJSONValue(key, raw)
		if err != nil {
			return err
		}
		out[key] = model.WakeSetting{Value: value, Source: source}
	}
	return nil
}

func wakeJSONValue(key string, raw json.RawMessage) (string, error) {
	var value string
	if key == "wake.live" || key == "inbox.strict_board_identity" {
		var flag bool
		if string(raw) == "null" {
			return "", invalid(key + " must be boolean")
		}
		if err := json.Unmarshal(raw, &flag); err != nil {
			return "", failure(key+" must be boolean", err)
		}
		value = strconv.FormatBool(flag)
	} else {
		var number int64
		if string(raw) == "null" {
			return "", invalid(key + " must be an integer")
		}
		if err := json.Unmarshal(raw, &number); err != nil {
			return "", failure(key+" must be an integer", err)
		}
		value = strconv.FormatInt(number, 10)
	}
	value, err := model.NormalizeWakeSetting(key, value)
	if err != nil {
		return "", fmt.Errorf("invalid stored wake setting: %w", err)
	}
	return value, nil
}

func wakeScope(raw map[string]json.RawMessage, agent string) (map[string]json.RawMessage, map[string]map[string]json.RawMessage, error) {
	host := map[string]json.RawMessage{}
	agents := map[string]map[string]json.RawMessage{}
	if field, ok := raw["host"]; ok {
		if string(field) == "null" {
			return nil, nil, invalid("wake host must be an object")
		}
		if err := json.Unmarshal(field, &host); err != nil {
			return nil, nil, failure("wake host must be an object", err)
		}
	}
	if field, ok := raw["agents"]; ok {
		if string(field) == "null" {
			return nil, nil, invalid("wake agents must be an object")
		}
		if err := json.Unmarshal(field, &agents); err != nil {
			return nil, nil, failure("wake agents must be objects", err)
		}
	}
	if agent == "" {
		return host, agents, nil
	}
	scope := agents[agent]
	if scope == nil {
		scope = map[string]json.RawMessage{}
	}
	return scope, agents, nil
}

func writeWakeScope(raw map[string]json.RawMessage, agent string, scope map[string]json.RawMessage, agents map[string]map[string]json.RawMessage) error {
	if agent == "" {
		encoded, err := json.Marshal(scope)
		if err != nil {
			return fmt.Errorf("encode host wake settings: %w", err)
		}
		raw["host"] = encoded
		return nil
	}
	agents[agent] = scope
	encoded, err := json.Marshal(agents)
	if err != nil {
		return fmt.Errorf("encode agent wake settings: %w", err)
	}
	raw["agents"] = encoded
	return nil
}

func wakeValueJSON(value string) json.RawMessage {
	if value == "" {
		return json.RawMessage("0")
	}
	return json.RawMessage(value)
}

func selectedAgentWakeValues(agents map[string]map[string]json.RawMessage, agent string) (map[string]model.WakeSetting, error) {
	selected := map[string]model.WakeSetting{}
	for id, scope := range agents {
		if scope == nil || !validLearnedID(id) {
			return nil, invalid("wake agent scope must name a valid agent and object")
		}
		target := map[string]model.WakeSetting{}
		if err := applyWakeValues(target, scope, "agent:"+id); err != nil {
			return nil, err
		}
		if id == agent {
			selected = target
		}
	}
	return selected, nil
}
