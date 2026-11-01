// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/hyper-swe/mtix/internal/model"
)

// WakeSettings reads effective policy without creating state (FR-20.3/20.5).
// Legacy flat envelope and strict-identity fields remain readable.
func (s *State) WakeSettings(agent string) (map[string]model.WakeSetting, []Report, error) {
	raw, reports, err := s.readWakeSettings()
	if err != nil {
		return nil, reports, err
	}
	values, err := effectiveWakeSettings(raw, agent)
	return values, reports, err
}

func (s *State) readWakeSettings() (map[string]json.RawMessage, []Report, error) {
	var encoded json.RawMessage
	reports, err := s.ReadJSON("wake", &encoded)
	if err != nil {
		return nil, reports, fmt.Errorf("read wake settings: %w", err)
	}
	if len(reports) > 0 {
		return nil, reports, invalid("wake.json belongs to another host")
	}
	raw := map[string]json.RawMessage{}
	if len(encoded) > 0 {
		if string(encoded) == "null" {
			return nil, reports, invalid("wake settings must be an object")
		}
		if err = json.Unmarshal(encoded, &raw); err != nil {
			return nil, reports, failure("wake settings must be an object", err)
		}
	}
	return raw, reports, nil
}

// WakeSettingUpdate changes one host or agent override; nil Value deletes it.
type WakeSettingUpdate struct {
	Agent, Key string
	Value      *string
}

// UpdateWakeSetting serializes an authorized scope change (FR-20.3).
func (s *State) UpdateWakeSetting(ctx context.Context, update WakeSettingUpdate, authorize func(string, string) error) (err error) {
	agent, key := update.Agent, update.Key
	if !model.IsWakeSetting(key) {
		return fmt.Errorf("unknown wake setting: %w", model.ErrInvalidConfigKey)
	}
	if agent != "" && !validLearnedID(agent) {
		return invalid("wake agent id is invalid")
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("start wake settings update: %w", ctxErr)
	}
	preview, _, err := s.readWakeSettings()
	if err != nil {
		return err
	}
	if updateErr := applyWakeSettingUpdate(preview, update, authorize); updateErr != nil {
		return updateErr
	}
	dir, err := s.directory(true)
	if err != nil {
		return err
	}
	defer closeWith(dir, &err)
	lock, err := openNamedLock(dir, "wake-settings.lock", true)
	if err != nil {
		return failure("wake settings lock", err)
	}
	if err = awaitAlertLock(ctx, lock); err != nil {
		return errors.Join(err, closeFile(lock))
	}
	defer func() { err = errors.Join(err, unlockAlert(lock)) }()
	raw, _, err := s.readWakeSettings()
	if err != nil {
		return err
	}
	if updateErr := applyWakeSettingUpdate(raw, update, authorize); updateErr != nil {
		return updateErr
	}
	return s.WriteJSON("wake", raw)
}

// WakeAgents lists configured scope names without creating any files.
func (s *State) WakeAgents() ([]string, error) {
	raw, _, err := s.readWakeSettings()
	if err != nil {
		return nil, err
	}
	_, agents, err := wakeScope(raw, "")
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(agents))
	for agent := range agents {
		if !validLearnedID(agent) {
			return nil, invalid("wake agent id is invalid")
		}
		keys = append(keys, agent)
	}
	sort.Strings(keys)
	return keys, nil
}

func applyWakeSettingUpdate(raw map[string]json.RawMessage, update WakeSettingUpdate, authorize func(string, string) error) error {
	agent, key, value := update.Agent, update.Key, update.Value
	before, err := effectiveWakeSettings(raw, agent)
	if err != nil {
		return err
	}
	scope, agents, err := wakeScope(raw, agent)
	if err != nil {
		return err
	}
	if value == nil {
		delete(scope, key)
	} else {
		scope[key] = wakeValueJSON(*value)
	}
	if writeErr := writeWakeScope(raw, agent, scope, agents); writeErr != nil {
		return writeErr
	}
	after, err := effectiveWakeSettings(raw, agent)
	if err != nil {
		return err
	}
	// Authorize the actual override too: a host live-off must not mask a latent agent live-on change.
	target := after[key].Value
	if value != nil {
		target = *value
	}
	if authorize != nil {
		if authErr := authorize(before[key].Value, target); authErr != nil {
			return authErr
		}
	}
	return nil
}
