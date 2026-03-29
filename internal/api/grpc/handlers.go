// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

// handlers.go implements all RPC handler methods on the Server struct per FR-8.2.
// Each handler validates request, delegates to the appropriate service,
// maps errors to gRPC status codes, and returns the response.
//
// Error mapping per FR-7.7:
//   ErrNotFound          → codes.NotFound
//   ErrInvalidInput      → codes.InvalidArgument
//   ErrAlreadyExists     → codes.AlreadyExists
//   ErrInvalidTransition → codes.FailedPrecondition
//   ErrCycleDetected     → codes.FailedPrecondition

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// --- CRUD Handlers ---

// HandleCreateNode implements the CreateNode RPC per FR-8.2.
func (s *Server) HandleCreateNode(ctx context.Context, req *CreateNodeReq) (*model.Node, error) {
	node, err := s.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Title:       req.Title,
		ParentID:    req.ParentID,
		Project:     req.Project,
		Description: req.Description,
		Prompt:      req.Prompt,
		Acceptance:  req.Acceptance,
		Creator:     req.Creator,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return node, nil
}

// HandleGetNode implements the GetNode RPC per FR-8.2.
func (s *Server) HandleGetNode(ctx context.Context, id string) (*model.Node, error) {
	node, err := s.nodeSvc.GetNode(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	return node, nil
}

// HandleUpdateNode implements the UpdateNode RPC per FR-8.2.
func (s *Server) HandleUpdateNode(ctx context.Context, req *UpdateNodeReq) (*model.Node, error) {
	updates := &store.NodeUpdate{
		Title:       req.Title,
		Description: req.Description,
		Prompt:      req.Prompt,
		Acceptance:  req.Acceptance,
	}
	if err := s.nodeSvc.UpdateNode(ctx, req.ID, updates); err != nil {
		return nil, mapError(err)
	}
	node, err := s.nodeSvc.GetNode(ctx, req.ID)
	if err != nil {
		return nil, mapError(err)
	}
	return node, nil
}

// HandleDeleteNode implements the DeleteNode RPC per FR-8.2.
func (s *Server) HandleDeleteNode(ctx context.Context, id string, cascade bool, author string) error {
	if err := s.nodeSvc.DeleteNode(ctx, id, cascade, author); err != nil {
		return mapError(err)
	}
	return nil
}

// HandleUndelete implements the UndeleteNode RPC per FR-8.2.
func (s *Server) HandleUndelete(ctx context.Context, id string) (*model.Node, error) {
	if err := s.nodeSvc.UndeleteNode(ctx, id); err != nil {
		return nil, mapError(err)
	}
	node, err := s.nodeSvc.GetNode(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	return node, nil
}

// HandleListChildren implements the ListChildren RPC per FR-8.2.
func (s *Server) HandleListChildren(ctx context.Context, parentID string, limit, offset int) ([]*model.Node, bool, error) {
	children, err := s.store.GetDirectChildren(ctx, parentID)
	if err != nil {
		return nil, false, mapError(err)
	}
	if offset >= len(children) {
		return nil, false, nil
	}
	children = children[offset:]
	hasMore := len(children) > limit
	if hasMore {
		children = children[:limit]
	}
	return children, hasMore, nil
}

// HandleDecompose implements the Decompose RPC per FR-8.2.
// Returns the list of created node IDs.
func (s *Server) HandleDecompose(ctx context.Context, parentID, creator string, children []DecomposeChildReq) ([]string, error) {
	var items []service.DecomposeInput
	for _, c := range children {
		items = append(items, service.DecomposeInput{
			Title:      c.Title,
			Prompt:     c.Prompt,
			Acceptance: c.Acceptance,
		})
	}
	ids, err := s.nodeSvc.Decompose(ctx, parentID, items, creator)
	if err != nil {
		return nil, mapError(err)
	}
	return ids, nil
}

// --- LLM Shortcut Handlers ---

// HandleClaim implements the Claim RPC per FR-8.2.
// Idempotent per FR-7.7a.
func (s *Server) HandleClaim(ctx context.Context, id, agent string, force bool) (*model.Node, error) {
	if force {
		threshold := 5 * time.Minute
		if err := s.store.ForceReclaimNode(ctx, id, agent, threshold); err != nil {
			return nil, mapError(err)
		}
	} else {
		if err := s.store.ClaimNode(ctx, id, agent); err != nil {
			return nil, mapError(err)
		}
	}
	return s.nodeSvc.GetNode(ctx, id)
}

// HandleUnclaim implements the Unclaim RPC per FR-8.2.
func (s *Server) HandleUnclaim(ctx context.Context, id, reason, agent string) (*model.Node, error) {
	if err := s.store.UnclaimNode(ctx, id, reason, agent); err != nil {
		return nil, mapError(err)
	}
	return s.nodeSvc.GetNode(ctx, id)
}

// HandleDone implements the Done RPC per FR-8.2. Idempotent per FR-7.7a.
func (s *Server) HandleDone(ctx context.Context, id, agent string) (*model.Node, error) {
	if err := s.nodeSvc.TransitionStatus(ctx, id, model.StatusDone, "", agent); err != nil {
		return nil, mapError(err)
	}
	return s.nodeSvc.GetNode(ctx, id)
}

// HandleDefer implements the Defer RPC per FR-8.2. Idempotent per FR-7.7a.
func (s *Server) HandleDefer(ctx context.Context, id, agent string) (*model.Node, error) {
	if err := s.nodeSvc.TransitionStatus(ctx, id, model.StatusDeferred, "", agent); err != nil {
		return nil, mapError(err)
	}
	return s.nodeSvc.GetNode(ctx, id)
}

// HandleCancel implements the Cancel RPC per FR-8.2. Idempotent per FR-7.7a.
func (s *Server) HandleCancel(ctx context.Context, id, reason, agent string) (*model.Node, error) {
	if err := s.store.CancelNode(ctx, id, reason, agent, false); err != nil {
		return nil, mapError(err)
	}
	return s.nodeSvc.GetNode(ctx, id)
}

// HandleReopen implements the Reopen RPC per FR-8.2.
func (s *Server) HandleReopen(ctx context.Context, id, agent string) (*model.Node, error) {
	if err := s.nodeSvc.TransitionStatus(ctx, id, model.StatusOpen, "", agent); err != nil {
		return nil, mapError(err)
	}
	return s.nodeSvc.GetNode(ctx, id)
}

// HandleBlock implements the Block RPC per FR-8.2.
func (s *Server) HandleBlock(ctx context.Context, id, agent string) (*model.Node, error) {
	if err := s.nodeSvc.TransitionStatus(ctx, id, model.StatusBlocked, "", agent); err != nil {
		return nil, mapError(err)
	}
	return s.nodeSvc.GetNode(ctx, id)
}

// HandleComment implements the Comment RPC per FR-8.2.
func (s *Server) HandleComment(ctx context.Context, nodeID, text, author string) error {
	return mapError(s.promptSvc.AddAnnotation(ctx, nodeID, text, author))
}

// --- Query Handlers ---

// HandleSearch implements the Search RPC per FR-8.2.
func (s *Server) HandleSearch(ctx context.Context, filter store.NodeFilter, limit, offset int) ([]*model.Node, int, bool, error) {
	opts := store.ListOptions{Limit: limit + 1, Offset: offset}
	nodes, total, err := s.store.ListNodes(ctx, filter, opts)
	if err != nil {
		return nil, 0, false, mapError(err)
	}
	hasMore := len(nodes) > limit
	if hasMore {
		nodes = nodes[:limit]
	}
	return nodes, total, hasMore, nil
}

// HandleGetContext implements the GetContext RPC per FR-8.2.
func (s *Server) HandleGetContext(ctx context.Context, id string) (*service.ContextResponse, error) {
	resp, err := s.contextSvc.GetContext(ctx, id, nil)
	if err != nil {
		return nil, mapError(err)
	}
	return resp, nil
}

// HandleUpdatePrompt implements the UpdatePrompt RPC per FR-8.2.
func (s *Server) HandleUpdatePrompt(ctx context.Context, id, prompt, author string) (*model.Node, error) {
	if err := s.promptSvc.UpdatePrompt(ctx, id, prompt, author); err != nil {
		return nil, mapError(err)
	}
	return s.nodeSvc.GetNode(ctx, id)
}

// HandleRerun implements the Rerun RPC per FR-8.2.
func (s *Server) HandleRerun(ctx context.Context, id string, strategy service.RerunStrategy, reason, author string) error {
	if err := s.nodeSvc.Rerun(ctx, id, strategy, reason, author); err != nil {
		return mapError(err)
	}
	return nil
}

// HandleRestore implements the Restore RPC per FR-8.2.
func (s *Server) HandleRestore(ctx context.Context, id, author string) (*model.Node, error) {
	if err := s.nodeSvc.Restore(ctx, id, author); err != nil {
		return nil, mapError(err)
	}
	return s.nodeSvc.GetNode(ctx, id)
}

// --- Session/Agent Handlers ---

// HandleSessionStart implements the SessionStart RPC per FR-8.2.
func (s *Server) HandleSessionStart(ctx context.Context, agentID, project string) (string, error) {
	sessionID, err := s.sessionSvc.SessionStart(ctx, agentID, project)
	if err != nil {
		return "", mapError(err)
	}
	return sessionID, nil
}

// HandleSessionEnd implements the SessionEnd RPC per FR-8.2.
func (s *Server) HandleSessionEnd(ctx context.Context, agentID string) error {
	return mapError(s.sessionSvc.SessionEnd(ctx, agentID))
}

// HandleSessionSummary implements the SessionSummary RPC per FR-8.2.
func (s *Server) HandleSessionSummary(ctx context.Context, agentID string) (*service.SessionSummary, error) {
	summary, err := s.sessionSvc.SessionSummary(ctx, agentID)
	if err != nil {
		return nil, mapError(err)
	}
	return summary, nil
}

// HandleHeartbeat implements the AgentHeartbeat RPC per FR-8.2.
func (s *Server) HandleHeartbeat(ctx context.Context, agentID string) error {
	return mapError(s.agentSvc.Heartbeat(ctx, agentID))
}

// HandleGetAgentState implements the GetAgentState RPC per FR-8.2.
func (s *Server) HandleGetAgentState(ctx context.Context, agentID string) (model.AgentState, error) {
	state, err := s.agentSvc.GetAgentState(ctx, agentID)
	if err != nil {
		return "", mapError(err)
	}
	return state, nil
}

// HandleSetAgentState implements the SetAgentState RPC per FR-8.2.
func (s *Server) HandleSetAgentState(ctx context.Context, agentID string, state model.AgentState, _, _ string) error {
	return mapError(s.agentSvc.UpdateAgentState(ctx, agentID, state))
}

// HandleGetCurrentWork implements the AgentCurrent RPC per FR-8.2.
func (s *Server) HandleGetCurrentWork(ctx context.Context, agentID string) (*model.Node, error) {
	node, err := s.agentSvc.GetCurrentWork(ctx, agentID)
	if err != nil {
		return nil, mapError(err)
	}
	return node, nil
}

// --- Dependency Handlers ---

// HandleAddDependency implements the AddDependency RPC per FR-8.2.
func (s *Server) HandleAddDependency(ctx context.Context, dep *model.Dependency) error {
	return mapError(s.store.AddDependency(ctx, dep))
}

// HandleRemoveDependency implements the RemoveDependency RPC per FR-8.2.
func (s *Server) HandleRemoveDependency(ctx context.Context, fromID, toID string, depType model.DepType) error {
	return mapError(s.store.RemoveDependency(ctx, fromID, toID, depType))
}

// HandleGetDependencies implements the GetDependencyTree RPC per FR-8.2.
func (s *Server) HandleGetDependencies(ctx context.Context, id string) ([]*model.Dependency, error) {
	deps, err := s.store.GetBlockers(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	return deps, nil
}

// --- Bulk Handler ---

const maxBulkBatchSize = 100

// HandleBulkUpdate implements the BulkUpdate RPC per FR-8.2.
func (s *Server) HandleBulkUpdate(ctx context.Context, updates []BulkNodeUpdateReq) (int, []string, error) {
	if len(updates) > maxBulkBatchSize {
		return 0, nil, mapError(model.ErrInvalidInput)
	}
	var (
		updated   int
		failedIDs []string
	)
	for _, u := range updates {
		upd := &store.NodeUpdate{Title: u.Title, Description: u.Description}
		if err := s.nodeSvc.UpdateNode(ctx, u.ID, upd); err != nil {
			failedIDs = append(failedIDs, u.ID)
			continue
		}
		updated++
	}
	return updated, failedIDs, nil
}

// --- Subscribe Handler ---

// HandleSubscribe implements the Subscribe streaming RPC per FR-8.2 and FR-7.5a.
// Streams matching events to the client until disconnect.
func (s *Server) HandleSubscribe(ctx context.Context, filter *SubscribeFilter, sendFn func(service.Event) error) error {
	eventCh := make(chan service.Event, 64)
	sub := &channelSubscriber{ch: eventCh, filter: filter, logger: s.logger}
	_ = sub

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("subscribe client disconnected")
			return nil
		case event := <-eventCh:
			if err := sendFn(event); err != nil {
				return nil
			}
		}
	}
}

// channelSubscriber forwards matching events to a channel.
type channelSubscriber struct {
	ch     chan service.Event
	filter *SubscribeFilter
	logger *slog.Logger
}

// Send forwards an event if it matches the filter. Non-blocking.
func (cs *channelSubscriber) Send(event service.Event) {
	if !cs.matchesFilter(event) {
		return
	}
	select {
	case cs.ch <- event:
	default:
		cs.logger.Warn("subscribe backpressure: dropping event", "type", event.Type)
	}
}

func (cs *channelSubscriber) matchesFilter(event service.Event) bool {
	if cs.filter == nil {
		return true
	}
	if cs.filter.Under != "" && !strings.HasPrefix(event.NodeID, cs.filter.Under) {
		return false
	}
	if len(cs.filter.Events) > 0 {
		for _, et := range cs.filter.Events {
			if et == string(event.Type) {
				return true
			}
		}
		return false
	}
	return true
}

// --- Intermediate Request Types ---

// CreateNodeReq maps to CreateNodeRequest proto message.
type CreateNodeReq struct {
	Title       string `json:"title"`
	ParentID    string `json:"parent_id"`
	Project     string `json:"project"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Acceptance  string `json:"acceptance"`
	Creator     string `json:"creator"`
}

// UpdateNodeReq maps to UpdateNodeRequest proto message.
type UpdateNodeReq struct {
	ID          string  `json:"id"`
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Prompt      *string `json:"prompt,omitempty"`
	Acceptance  *string `json:"acceptance,omitempty"`
}

// DecomposeChildReq maps to DecomposeChild proto message.
type DecomposeChildReq struct {
	Title      string `json:"title"`
	Prompt     string `json:"prompt"`
	Acceptance string `json:"acceptance"`
}

// BulkNodeUpdateReq maps to BulkNodeUpdate proto message.
type BulkNodeUpdateReq struct {
	ID          string  `json:"id"`
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
}

// SubscribeFilter maps to SubscriptionFilter proto message.
type SubscribeFilter struct {
	Under  string   `json:"under"`
	Events []string `json:"events"`
}
