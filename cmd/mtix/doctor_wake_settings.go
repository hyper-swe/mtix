// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func registerWakeSettingsDoctor(svc *service.DoctorService) error {
	if app.configSvc == nil {
		return nil
	}
	cs := app.configSvc
	if err := svc.Register(service.DoctorCheck{ID: "wake.settings", Run: func(context.Context) service.DoctorResult { return wakeSettingsDoctorRow(cs) }}); err != nil {
		return err
	}
	return svc.Register(service.DoctorCheck{ID: "settings.ignored-workspace-key", Run: func(context.Context) service.DoctorResult { return ignoredWorkspaceSettingsRow(cs) }})
}

func wakeSettingsDoctorRow(cs *service.ConfigService) service.DoctorResult {
	host, err := cs.WakeSettings()
	if err != nil {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: "Host wake settings are unreadable: " + err.Error(), Fix: "Restore an operator-owned wake.json with this host's host_id; then run mtix config get wake.live and mtix doctor."}
	}
	keys := make([]string, 0, len(host))
	for key := range host {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var descriptions []string
	for _, key := range keys {
		value := host[key].Value
		if value == "" {
			value = "unlimited"
		}
		descriptions = append(descriptions, key+"="+value+" ("+host[key].Source+")")
	}
	agents, err := cs.WakeAgentSettings()
	if err != nil {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: "Agent wake settings are unreadable: " + err.Error(), Fix: "Restore valid agent overrides in operator-local wake.json; then run mtix config get wake.live and mtix doctor."}
	}
	for agent, settings := range agents {
		for _, key := range keys {
			value := settings[key]
			if value.Source == "agent:"+agent {
				descriptions = append(descriptions, agent+": "+key+"="+value.Value+" ("+value.Source+")")
			}
		}
	}
	sort.Strings(descriptions)
	return service.DoctorResult{Status: service.DoctorInfo, Detail: strings.Join(descriptions, "; "), Data: map[string]any{"host": host, "agents": agents}, Fix: "No routine tuning is needed. To stop live wakes: mtix config set wake.live off"}
}

func ignoredWorkspaceSettingsRow(cs *service.ConfigService) service.DoctorResult {
	keys := cs.IgnoredWorkspaceKeys()
	if len(keys) == 0 {
		return service.DoctorResult{Status: service.DoctorInfo, Detail: "No workspace wake authority keys are used."}
	}
	var fixes []string
	defaults := model.WakeSettingDefaults()
	for _, key := range keys {
		if value, ok := defaults[key]; ok {
			if value == "" {
				value = "unset"
			}
			fixes = append(fixes, "mtix config set "+key+" "+value)
		}
	}
	return service.DoctorResult{Status: service.DoctorWarn, Detail: fmt.Sprintf("Ignored workspace authority keys: %q", keys), Fix: "Remove those keys from .mtix/config.yaml; configure desired values operator-locally, for example " + strings.Join(fixes, "; "), Data: map[string]any{"keys": keys}}
}
