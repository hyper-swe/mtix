// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/hyper-swe/mtix/internal/format"
)

// Bound long-lived waits independently of the serial ordinary-tool route.
const maxConcurrentInboxWaits = 16

// Bound numeric normalization before math/big expands an exponent or mantissa.
const maxNumericRequestIDSize = 1024

type inboxWaitRequest struct {
	cancel    context.CancelFunc
	cancelled bool
}

type inboxWaitSession struct {
	ctx     context.Context
	mu      sync.Mutex
	active  map[string]*inboxWaitRequest
	running int
	workers sync.WaitGroup
}

func newInboxWaitSession(ctx context.Context) *inboxWaitSession {
	return &inboxWaitSession{ctx: ctx, active: make(map[string]*inboxWaitRequest)}
}

// requestIDKey retains numeric/string identity without float rounding or
// confusing equivalent escaped strings. MCP numeric request IDs are integers.
func requestIDKey(raw json.RawMessage) (string, bool) {
	var text string
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	if json.Unmarshal(raw, &text) == nil {
		return "s:" + text, true
	}
	if !json.Valid(raw) {
		return "", false
	}
	if !numericRequestIDFitsBudget(raw) {
		return "", false
	}
	value, ok := new(big.Rat).SetString(string(bytes.TrimSpace(raw)))
	if !ok || !value.IsInt() {
		return "", false
	}
	return "n:" + value.Num().String(), true
}

func numericRequestIDFitsBudget(raw json.RawMessage) bool {
	text := string(bytes.TrimSpace(raw))
	if len(text) > maxNumericRequestIDSize {
		return false
	}
	index := strings.IndexAny(text, "eE")
	if index < 0 {
		return true
	}
	exponent, err := strconv.ParseInt(text[index+1:], 10, 16)
	return err == nil && exponent >= -maxNumericRequestIDSize && exponent <= maxNumericRequestIDSize
}

func (session *inboxWaitSession) hasID(id json.RawMessage) bool {
	key, ok := requestIDKey(id)
	if !ok {
		return false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.active[key] != nil
}

func (s *Server) isWaitRequest(req *Request) bool {
	var params ToolsCallParams
	return json.Unmarshal(req.Params, &params) == nil && s.tools.isCancellableWait(params.Name)
}

func (s *Server) dispatchInboxWait(req *Request) error {
	key, valid := requestIDKey(req.ID)
	if !valid {
		return s.sendError(req.ID, ErrCodeInvalidRequest, "invalid inbox wait request ID")
	}
	session := s.waits
	session.mu.Lock()
	if session.active[key] != nil {
		session.mu.Unlock()
		return s.sendError(req.ID, ErrCodeInvalidRequest, "duplicate active request ID")
	}
	if session.running >= maxConcurrentInboxWaits {
		session.mu.Unlock()
		return s.sendResult(req.ID, ErrorResult("too many active inbox waits"))
	}
	ctx, cancel := context.WithCancel(session.ctx)
	wait := &inboxWaitRequest{cancel: cancel}
	session.active[key] = wait
	session.running++
	session.workers.Add(1)
	session.mu.Unlock()
	go s.runInboxWait(ctx, session, key, wait, req)
	return nil
}

func (s *Server) runInboxWait(ctx context.Context, session *inboxWaitSession, key string, wait *inboxWaitRequest, req *Request) {
	defer session.workerDone()
	defer wait.cancel()
	var params ToolsCallParams
	// Dispatch validated the envelope. Keep tool argument validation in its handler.
	var result *ToolsCallResult
	err := json.Unmarshal(req.Params, &params)
	if err == nil {
		result, err = s.tools.Call(ctx, params.Name, params.Arguments)
	}
	if err != nil {
		result = ErrorResult(format.SafeText(err.Error()))
	}
	if !session.complete(key, wait) {
		return
	}
	if err := s.sendResult(req.ID, result); err != nil {
		s.logger.Error("write inbox wait response", "error", err)
	}
}

func (session *inboxWaitSession) workerDone() {
	session.mu.Lock()
	session.running--
	session.mu.Unlock()
	session.workers.Done()
}

func (session *inboxWaitSession) complete(key string, wait *inboxWaitRequest) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.active[key] == wait {
		delete(session.active, key)
	}
	// Completion linearizes here. A later cancellation may be ignored; a client
	// must tolerate a response racing cancellation per MCP 2024-11-05.
	return !wait.cancelled && session.ctx.Err() == nil
}

func (session *inboxWaitSession) cancelRequest(params json.RawMessage) {
	var notification struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(params, &notification) != nil {
		return
	}
	key, valid := requestIDKey(notification.RequestID)
	if !valid {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if wait := session.active[key]; wait != nil {
		wait.cancelled = true
		wait.cancel()
		delete(session.active, key)
	}
}

func (s *Server) interruptIO(ctx context.Context) func() error {
	_, inputClosable := s.reader.(io.Closer)
	_, outputClosable := s.writer.(io.Closer)
	if !inputClosable && !outputClosable {
		return func() error { return nil }
	}
	closed := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		<-ctx.Done()
		// Do not acquire the response mutex: a blocked Write may hold it.
		inputErr := closeSessionStream(s.reader, "input")
		outputErr := closeSessionStream(s.writer, "output")
		closed <- errors.Join(inputErr, outputErr)
	}()
	return func() error {
		err := <-closed
		<-joined
		return err
	}
}

func closeSessionStream(stream any, name string) error {
	closer, ok := stream.(io.Closer)
	if !ok {
		return nil
	}
	err := closer.Close()
	if err == nil || errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
		return nil
	}
	return fmt.Errorf("close session %s: %w", name, err)
}
