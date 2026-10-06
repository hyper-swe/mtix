// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The FR-10.4 process harness records invocation/response order in the parent.
// Each re-executed test process opens its own real SQLite Store and NodeService.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anishathalye/porcupine"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func newClaimService(t *testing.T, path string) *service.NodeService {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := sqlite.New(path, logger)
	require.NoError(t, err)
	st.SetClock(testClock())
	t.Cleanup(func() { require.NoError(t, st.Close(), "close owned SQLite store") })
	return service.NewNodeService(st, &service.NoopBroadcaster{}, &service.StaticConfig{}, logger, testClock())
}

// TestClaimProcess_Helper is only active in explicitly re-executed children.
func TestClaimProcess_Helper(t *testing.T) {
	if os.Getenv("MTIX_CLAIM_HELPER") != "1" {
		return
	}
	svc := newClaimService(t, os.Getenv("MTIX_CLAIM_DB"))
	encoder := json.NewEncoder(os.Stdout)
	require.NoError(t, encoder.Encode("ready"))
	var release string
	require.NoError(t, json.NewDecoder(os.Stdin).Decode(&release))
	require.Equal(t, "claim", release)
	err := svc.ClaimNode(context.Background(), os.Getenv("MTIX_CLAIM_NODE"), os.Getenv("MTIX_CLAIM_AGENT"))
	output := claimOutput{Result: "success"}
	if errors.Is(err, model.ErrAlreadyClaimed) {
		output.Result = "already_claimed"
	} else {
		require.NoError(t, err)
	}
	require.NoError(t, encoder.Encode(output))
}

type claimProcess struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	decoder *json.Decoder
	joined  bool
}

func startClaimProcess(t *testing.T, path, nodeID, agent string) *claimProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestClaimProcess_Helper$")
	cmd.Env = append(os.Environ(), "MTIX_CLAIM_HELPER=1", "MTIX_CLAIM_DB="+path, "MTIX_CLAIM_NODE="+nodeID, "MTIX_CLAIM_AGENT="+agent)
	// Leave verbose testing banners off: stdout carries the result protocol.
	cmd.Stderr = os.Stderr
	process := &claimProcess{cmd: cmd, cancel: cancel}
	t.Cleanup(func() { process.cleanup(t) })
	var err error
	process.stdin, err = cmd.StdinPipe()
	require.NoError(t, err)
	process.stdout, err = cmd.StdoutPipe()
	require.NoError(t, err)
	process.decoder = json.NewDecoder(process.stdout)
	require.NoError(t, cmd.Start())
	t.Logf("owned claim helper PID %d, agent %s", cmd.Process.Pid, agent)
	var ready string
	require.NoError(t, process.decoder.Decode(&ready), "helper startup")
	require.Equal(t, "ready", ready)
	return process
}

func closeClaimPipe(pipe io.Closer) error {
	if pipe == nil {
		return nil
	}
	err := pipe.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func (process *claimProcess) cleanup(t *testing.T) {
	t.Helper()
	process.cancel()
	if process.cmd.Process != nil && !process.joined {
		err := process.cmd.Wait()
		process.joined = true
		// Cancellation on an already failing test is an intentional owned shutdown.
		if err != nil {
			t.Logf("joined cancelled owned helper: %v", err)
		}
	}
	require.NoError(t, closeClaimPipe(process.stdin), "close helper input")
	require.NoError(t, closeClaimPipe(process.stdout), "close helper output")
}

type recordedClaim struct {
	operation porcupine.Operation
	err       error
}

func (process *claimProcess) invoke(agent string, call int64, sequence *atomic.Int64, release <-chan struct{}) recordedClaim {
	<-release
	if err := json.NewEncoder(process.stdin).Encode("claim"); err != nil {
		return recordedClaim{err: fmt.Errorf("release claim: %w", err)}
	}
	if err := closeClaimPipe(process.stdin); err != nil {
		return recordedClaim{err: fmt.Errorf("close release: %w", err)}
	}
	var output claimOutput
	if err := process.decoder.Decode(&output); err != nil {
		return recordedClaim{err: fmt.Errorf("claim response: %w", err)}
	}
	response := sequence.Add(1)
	return recordedClaim{operation: porcupine.Operation{Input: claimInput{Kind: "claim", Agent: agent}, Output: output, Call: call, Return: response}}
}

func recordProcessClaimHistory(t *testing.T) []porcupine.Operation {
	t.Helper()
	path := t.TempDir()
	svc := newClaimService(t, path)
	node, err := svc.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "TEST", Title: "Two processes compete for one claim"})
	require.NoError(t, err)
	agents := []string{"claim-agent-a", "claim-agent-b"}
	// Startup/migrations are serialized; only the actual claim invocations overlap.
	processes := []*claimProcess{startClaimProcess(t, path, node.ID, agents[0])}
	processes = append(processes, startClaimProcess(t, path, node.ID, agents[1]))
	var sequence atomic.Int64
	release := make(chan struct{})
	results := make(chan recordedClaim, len(processes))
	for index, process := range processes {
		call := sequence.Add(1)
		go func() { results <- process.invoke(agents[index], call, &sequence, release) }()
	}
	close(release)
	history := collectClaimResults(t, processes, results)
	readCall := sequence.Add(1)
	final, readErr := svc.GetNode(context.Background(), node.ID)
	require.NoError(t, readErr)
	history = append(history, porcupine.Operation{Input: claimInput{Kind: "read"}, Call: readCall,
		Output: claimOutput{Result: "read", Owner: final.Assignee, Status: final.Status}, Return: sequence.Add(1)})
	return history
}

func collectClaimResults(t *testing.T, processes []*claimProcess, results <-chan recordedClaim) []porcupine.Operation {
	t.Helper()
	history := make([]porcupine.Operation, 0, len(processes)+1)
	// Drain every result before asserting: no child or receiver goroutine is left.
	var resultErr error
	for range processes {
		result := <-results
		resultErr = errors.Join(resultErr, result.err)
		history = append(history, result.operation)
	}
	for _, process := range processes {
		waitErr := process.cmd.Wait()
		process.joined = true
		resultErr = errors.Join(resultErr, waitErr)
	}
	require.NoError(t, resultErr)
	successes, refusals := 0, 0
	for _, op := range history {
		if op.Output == (claimOutput{Result: "success"}) {
			successes++
		}
		if op.Output == (claimOutput{Result: "already_claimed"}) {
			refusals++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, refusals)
	return history
}
