// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Claim histories exercise FR-10.4 through real processes and SQLite stores.
// Porcupine checks the recorded calls against an immutable sequential model.
package integration

import (
	"testing"
	"time"

	"github.com/anishathalye/porcupine"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

type claimInput struct{ Kind, Agent string }
type claimOutput struct {
	Result, Owner string
	Status        model.Status
}
type claimState struct{ Owner string }

func claimSequentialModel() porcupine.Model {
	return porcupine.Model{
		Init:  func() interface{} { return claimState{} },
		Step:  claimModelStep,
		Equal: func(left, right interface{}) bool { return left == right },
	}
}

func claimModelStep(state, input, output interface{}) (bool, interface{}) {
	current, stateOK := state.(claimState)
	request, inputOK := input.(claimInput)
	response, outputOK := output.(claimOutput)
	if !stateOK || !inputOK || !outputOK {
		return false, state
	}
	switch request.Kind {
	case "claim":
		if request.Agent == "" {
			return false, current
		}
		if current.Owner == "" {
			return response == (claimOutput{Result: "success"}), claimState{Owner: request.Agent}
		}
		return response == (claimOutput{Result: "already_claimed"}), current
	case "read":
		status := model.StatusOpen
		if current.Owner != "" {
			status = model.StatusInProgress
		}
		return response == (claimOutput{Result: "read", Owner: current.Owner, Status: status}), current
	default:
		return false, current
	}
}

func requireClaimVerdict(t *testing.T, history []porcupine.Operation, want porcupine.CheckResult) {
	t.Helper()
	// Unknown is never accepted as evidence of a valid or illegal history.
	got := porcupine.CheckOperationsTimeout(claimSequentialModel(), history, 5*time.Second)
	require.True(t, claimVerdictMatches(got, want), "checker returned %s, want %s; recorded history: %+v", got, want, history)
}

func TestIntegration_ConcurrentProcessClaimsAreLinearizable(t *testing.T) {
	history := recordProcessClaimHistory(t)
	requireClaimVerdict(t, history, porcupine.Ok)
	t.Logf("actual two-process history: %+v", history)
	cases := []struct {
		name   string
		change func([]porcupine.Operation)
	}{
		{"second successful claim", func(ops []porcupine.Operation) {
			for index := range ops {
				if ops[index].Output == (claimOutput{Result: "already_claimed"}) {
					ops[index].Output = claimOutput{Result: "success"}
				}
			}
		}},
		{"wrong final owner", func(ops []porcupine.Operation) {
			ops[len(ops)-1].Output = claimOutput{Result: "read", Owner: "wrong-agent", Status: model.StatusInProgress}
		}},
		{"empty final read", func(ops []porcupine.Operation) {
			ops[len(ops)-1].Output = claimOutput{Result: "read", Status: model.StatusOpen}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := append([]porcupine.Operation(nil), history...)
			tc.change(changed)
			requireClaimVerdict(t, changed, porcupine.Illegal)
		})
	}
}

func TestClaimModel_SequentialValidHistoryIsAccepted(t *testing.T) {
	history := []porcupine.Operation{
		{Input: claimInput{Kind: "read"}, Output: claimOutput{Result: "read", Status: model.StatusOpen}, Call: 1, Return: 2},
		{Input: claimInput{Kind: "claim", Agent: "agent-a"}, Output: claimOutput{Result: "success"}, Call: 3, Return: 4},
		{Input: claimInput{Kind: "claim", Agent: "agent-b"}, Output: claimOutput{Result: "already_claimed"}, Call: 5, Return: 6},
		{Input: claimInput{Kind: "read"}, Output: claimOutput{Result: "read", Owner: "agent-a", Status: model.StatusInProgress}, Call: 7, Return: 8},
	}
	requireClaimVerdict(t, history, porcupine.Ok)
}

func TestClaimModel_InvalidInputsAndOutputsAreRejected(t *testing.T) {
	cases := []struct {
		name                 string
		state, input, output interface{}
	}{
		{"wrong state type", nil, claimInput{}, claimOutput{}},
		{"wrong input type", claimState{}, nil, claimOutput{}},
		{"wrong output type", claimState{}, claimInput{}, nil},
		{"empty agent", claimState{}, claimInput{Kind: "claim"}, claimOutput{Result: "success"}},
		{"unexpected claim failure", claimState{}, claimInput{Kind: "claim", Agent: "agent-a"}, claimOutput{Result: "database_error"}},
		{"unexpected operation", claimState{}, claimInput{Kind: "release"}, claimOutput{Result: "success"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			valid, _ := claimModelStep(tc.state, tc.input, tc.output)
			require.False(t, valid)
		})
	}
}

func claimVerdictMatches(got, want porcupine.CheckResult) bool {
	return got != porcupine.Unknown && got == want
}

func TestClaimModel_UnknownNeverProvesLinearizability(t *testing.T) {
	cases := []struct {
		name      string
		got, want porcupine.CheckResult
		accepted  bool
	}{
		{"valid", porcupine.Ok, porcupine.Ok, true},
		{"illegal", porcupine.Illegal, porcupine.Illegal, true},
		{"unknown valid", porcupine.Unknown, porcupine.Ok, false},
		{"unknown illegal", porcupine.Unknown, porcupine.Illegal, false},
		{"unknown expected", porcupine.Unknown, porcupine.Unknown, false},
		{"illegal is not valid", porcupine.Illegal, porcupine.Ok, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.accepted, claimVerdictMatches(tc.got, tc.want)) })
	}
}
