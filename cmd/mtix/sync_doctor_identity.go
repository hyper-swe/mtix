// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// appendBoardIdentityChecks reads local identity evidence without contacting a transport.
// The sticky hub-connected mark follows the identity checks (MTIX-112.17.3, ADR-007 A9).
func appendBoardIdentityChecks(report DoctorReport) DoctorReport {
	report = appendBoardIDChecks(report)
	return appendHubMarkCheck(context.Background(), report)
}

// appendBoardIDChecks appends the board identity checks alone.
func appendBoardIDChecks(report DoctorReport) DoctorReport {
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
	ids, early := readDoctorBoardIDs(observation)
	if early != nil {
		return appendDoctorCheck(report, *early)
	}
	connected := identityBoardConnected(observation)
	if connected && !observation.Hub.Known() {
		return appendDoctorCheck(report, unknownHubCheck(observation))
	}
	report = appendDoctorCheck(report, DoctorCheck{Name: "board.id", Pass: true, Detail: "one committed board identity"})
	return appendIdentityChangeCheck(report, ids[0], observation, grants)
}

// readDoctorBoardIDs returns the committed ids, or the single check that ends the doctor pass.
func readDoctorBoardIDs(observation oplocal.BoardObservation) ([]string, *DoctorCheck) {
	ids, err := service.ReadBoardIDs(app.mtixDir)
	connected := identityBoardConnected(observation)
	if errors.Is(err, os.ErrNotExist) && !connected {
		return nil, &DoctorCheck{Name: "board.id", Pass: true, Detail: "not required for a board without a hub observation"}
	}
	if errors.Is(err, os.ErrNotExist) {
		// This build cannot create the file (setup arrives in a later release), so an
		// existing hub board without it is advisory, never an un-clearable failure.
		return nil, &DoctorCheck{Name: "board.id", Pass: true, Warn: true, Detail: "no committed board identity (.mtix/board-id) yet; this mtix build does not create one for an existing board, and it must not be created by hand"}
	}
	if err != nil {
		check := boardIdentityFailure(err.Error(), "commit one valid .mtix/board-id")
		return nil, &check
	}
	if len(ids) != 1 {
		check := boardIdentityFailure("multiple board identities; retain one committed identity before re-confirming grants", "resolve .mtix/board-id to one committed UUID")
		return nil, &check
	}
	return ids, nil
}

// unknownHubCheck separates "no pull has recorded the hub yet" (a warning that
// one pull clears) from "a pull observed no hub row or an empty hash" (a failure).
func unknownHubCheck(observation oplocal.BoardObservation) DoctorCheck {
	if observation.Checkout == "" {
		return DoctorCheck{Name: "board.id", Pass: true, Warn: true, Detail: "hub identity not recorded yet; no pull has observed it", Fix: "mtix sync pull"}
	}
	return boardIdentityFailure("hub identity is unknown", "mtix sync init")
}

func boardIdentityFailure(detail, fix string) DoctorCheck {
	return DoctorCheck{Name: "board.id", Detail: detail, Fix: fix}
}

// identityBoardConnected answers "is this board connected to a hub or relay"
// for board.id. A hub observation a pull recorded counts, because it is
// evidence of its own (the mark follows at the next sync). Otherwise the
// answer is the hub mark's: the stored mark, applied events above the reset
// floor, sync or relay config, or a secrets DSN. A DSN that is only exported
// never counts, so board.id and board.hub-mark agree on it (ADR-007 A9).
func identityBoardConnected(observation oplocal.BoardObservation) bool {
	return observation.Checkout != "" || hubMarkConnected()
}

func appendIdentityChangeCheck(report DoctorReport, id string, observation oplocal.BoardObservation, grants []oplocal.Grant) DoctorReport {
	if observation.BoardID != "" && observation.BoardID != id {
		return appendDoctorCheck(report, boardIdentityFailure("multiple board identities observed for this checkout", "resolve .mtix/board-id to one committed UUID"))
	}
	for _, grant := range grants {
		if grant.BoardID == id && grant.Hub != observation.Hub {
			return appendDoctorCheck(report, DoctorCheck{Name: "board.hub-identity-changed", Pass: true, Warn: true, Detail: "observed hub identity differs from the recorded confirmation; re-confirmation of the hub is not available in this build, and the optional strict matching setting inbox.strict_board_identity is not settable by a command in this build"})
		}
	}
	return report
}
