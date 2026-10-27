// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// WakeConfigOptions injects host storage, optional agent scope and TTY policy.
type WakeConfigOptions struct {
	State      *oplocal.State
	Agent      string
	RequireTTY func() error
}

// WithWakeSettings returns a scoped config view without changing the original.
func (cs *ConfigService) WithWakeSettings(options WakeConfigOptions) *ConfigService {
	scoped := *cs
	scoped.wake = &options
	return &scoped
}

func (cs *ConfigService) wakeOptions() (WakeConfigOptions, error) {
	if cs.wake != nil {
		if err := validateAgentID(cs.wake.Agent); err != nil {
			return WakeConfigOptions{}, err
		}
		if cs.wake.State == nil {
			return WakeConfigOptions{}, fmt.Errorf("wake settings storage is required: %w", model.ErrInvalidInput)
		}
		return *cs.wake, nil
	}
	env, err := oplocal.FromProcess()
	if err != nil {
		return WakeConfigOptions{}, err
	}
	return WakeConfigOptions{State: oplocal.New(env)}, nil
}

// WakeSettings returns the effective scoped values and their source for doctor.
func (cs *ConfigService) WakeSettings() (map[string]model.WakeSetting, error) {
	options, err := cs.wakeOptions()
	if err != nil {
		return nil, err
	}
	values, _, err := options.State.WakeSettings(options.Agent)
	return values, err
}

func (cs *ConfigService) getWakeSetting(key string) (string, error) {
	values, err := cs.WakeSettings()
	if err != nil {
		return "", err
	}
	return values[key].Value, nil
}

func (cs *ConfigService) changeWakeSetting(key string, value *string) error {
	if cs.wake == nil {
		return fmt.Errorf("host wake settings require mtix config set or mtix config delete: %w", model.ErrInvalidInput)
	}
	options, err := cs.wakeOptions()
	if err != nil {
		return err
	}
	tty := options.RequireTTY
	if tty == nil {
		tty = func() error {
			return fmt.Errorf("use mtix config set from an operator terminal: %w", model.ErrInvalidInput)
		}
	}
	authorize := func(before, after string) error {
		if model.WakeSettingLoosens(key, before, after) {
			if ttyErr := tty(); ttyErr != nil {
				return fmt.Errorf("loosening %s requires operator terminal: %w", key, ttyErr)
			}
		}
		return nil
	}
	// Reject a plainly unauthorized change before even creating a settings lock.
	current, _, err := options.State.WakeSettings(options.Agent)
	if err != nil {
		return err
	}
	if value != nil {
		if authErr := authorize(current[key].Value, *value); authErr != nil {
			return authErr
		}
	}
	return options.State.UpdateWakeSetting(context.Background(), oplocal.WakeSettingUpdate{Agent: options.Agent, Key: key, Value: value}, authorize)
}

func (cs *ConfigService) setWakeSetting(key, value string) (string, error) {
	normalized, err := model.NormalizeWakeSetting(key, value)
	if err != nil {
		return "", err
	}
	return "", cs.changeWakeSetting(key, &normalized)
}

// IgnoredWorkspaceKeys lists authority settings ignored in config.yaml.
func (cs *ConfigService) IgnoredWorkspaceKeys() []string {
	keys := make([]string, 0, len(cs.ignoredWake))
	for key := range cs.ignoredWake {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ignoredWorkspaceKey(key string) bool {
	return strings.HasPrefix(key, "wake.") || key == "inbox.strict_board_identity"
}

func validConfigKey(key string) bool { return validConfigKeys[key] || model.IsWakeSetting(key) }

// WakeAgentSettings returns effective values for all configured agent overrides.
func (cs *ConfigService) WakeAgentSettings() (map[string]map[string]model.WakeSetting, error) {
	options, err := cs.wakeOptions()
	if err != nil {
		return nil, err
	}
	if _, _, validationErr := options.State.WakeSettings(""); validationErr != nil {
		return nil, validationErr
	}
	agents, err := options.State.WakeAgents()
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]model.WakeSetting{}
	for _, agent := range agents {
		values, _, err := options.State.WakeSettings(agent)
		if err != nil {
			return nil, err
		}
		out[agent] = values
	}
	return out, nil
}

// IgnoredWorkspaceSettings returns a copy of ignored values for exact operator remedies.
// Values are untrusted; callers must validate them before including commands.
func (cs *ConfigService) IgnoredWorkspaceSettings() map[string]string {
	values := make(map[string]string, len(cs.ignoredWake))
	for key, value := range cs.ignoredWake {
		values[key] = value
	}
	return values
}
