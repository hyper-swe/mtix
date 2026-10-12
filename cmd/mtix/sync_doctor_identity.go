// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// appendBoardIdentityChecks reads local identity evidence without contacting a transport.
func appendBoardIdentityChecks(report DoctorReport) DoctorReport {
	env, err := oplocal.FromProcess()
	if err != nil {
		return appendDoctorCheck(report, boardIdentityFailure(err.Error(), "mtix hooks status --json"))
	}
	state := oplocal.New(env)
	checkout := filepath.Clean(filepath.Dir(app.mtixDir))
	observation, _, err := state.BoardObservation(checkout)
	if err != nil {
		return appendDoctorCheck(report, boardIdentityFailure(err.Error(), "mtix hooks status --json"))
	}
	if observation.Hub.Kind == "relay" {
		configured, pathErr := filepath.Abs(relayDir())
		if pathErr != nil || relayDir() == "" || observation.RelayPath != configured {
			observation.Hub = oplocal.HubIdentity{}
		}
	}
	grants, _, err := state.BoardGrants()
	if err != nil {
		return appendDoctorCheck(report, boardIdentityFailure(err.Error(), "mtix hooks status --json"))
	}
	if _, _, err = state.StrictBoardIdentity(); err != nil {
		return appendDoctorCheck(report, boardIdentityFailure(err.Error(), "mtix hooks status --json"))
	}
	ids, err := service.ReadBoardIDs(app.mtixDir)
	connected := identityBoardConnected(observation)
	if errors.Is(err, os.ErrNotExist) && !connected {
		return appendDoctorCheck(report, DoctorCheck{Name: "board.id", Pass: true, Detail: "not required for a board without a hub observation"})
	}
	if err != nil {
		return appendDoctorCheck(report, boardIdentityFailure(err.Error(), "commit one valid .mtix/board-id"))
	}
	if len(ids) != 1 {
		return appendDoctorCheck(report, boardIdentityFailure("multiple board identities; retain one committed identity before re-confirming grants", "resolve .mtix/board-id to one committed UUID"))
	}
	if connected && !observation.Hub.Known() {
		return appendDoctorCheck(report, boardIdentityFailure("hub identity is unknown", "mtix sync init"))
	}
	report = appendDoctorCheck(report, DoctorCheck{Name: "board.id", Pass: true, Detail: "one committed board identity"})
	return appendIdentityChangeCheck(report, ids[0], observation, grants)
}

func boardIdentityFailure(detail, fix string) DoctorCheck {
	return DoctorCheck{Name: "board.id", Detail: detail, Fix: fix}
}

func identityBoardConnected(observation oplocal.BoardObservation) bool {
	if observation.Checkout != "" || relayDir() != "" || hubConfigured(app.mtixDir) {
		return true
	}
	if app.configSvc != nil {
		configured, err := app.configSvc.Get("sync.enabled")
		return err == nil && configured == "true"
	}
	return false
}

func appendIdentityChangeCheck(report DoctorReport, id string, observation oplocal.BoardObservation, grants []oplocal.Grant) DoctorReport {
	if observation.BoardID != "" && observation.BoardID != id {
		return appendDoctorCheck(report, boardIdentityFailure("multiple board identities observed for this checkout", "resolve .mtix/board-id to one committed UUID"))
	}
	for _, grant := range grants {
		if grant.BoardID == id && grant.Hub != observation.Hub {
			return appendDoctorCheck(report, DoctorCheck{Name: "board.hub-identity-changed", Pass: true, Warn: true, Detail: "observed hub identity differs from the recorded confirmation", Fix: "mtix setup in this checkout to re-confirm the hub, or mtix config set inbox.strict_board_identity true"})
		}
	}
	return report
}
