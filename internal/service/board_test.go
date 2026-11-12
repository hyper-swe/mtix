// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// boardStore answers ListNodes per status from a fixed table and records the
// filters and options it was called with.
type boardStore struct {
	mockStore
	byStatus map[model.Status][]*model.Node
	calls    []store.NodeFilter
	opts     []store.ListOptions
	err      error
}

func (b *boardStore) ListNodes(_ context.Context, f store.NodeFilter, o store.ListOptions) ([]*model.Node, int, error) {
	b.calls = append(b.calls, f)
	b.opts = append(b.opts, o)
	if b.err != nil {
		return nil, 0, b.err
	}
	all := b.byStatus[f.Status[0]]
	out := all
	if o.Limit > 0 && len(out) > o.Limit {
		out = out[:o.Limit]
	}
	return out, len(all), nil
}

func boardNode(id, title string, prio int, assignee string) *model.Node {
	return &model.Node{ID: id, Title: title, Priority: model.Priority(prio), Assignee: assignee}
}

func newBoardSvc(b *boardStore) *service.NodeService {
	return service.NewNodeService(b, nil, nil, nil, time.Now)
}

// MTIX-255.1: columns are the full status set in a fixed order, empty ones included.
func TestBoard_EmptyBoard_ReturnsAllColumnsWithZeroCounts(t *testing.T) {
	b := &boardStore{}
	got, err := newBoardSvc(b).Board(context.Background(), service.BoardRequest{Limit: 5})
	require.NoError(t, err)
	var names []string
	for _, c := range got.Columns {
		names = append(names, c.Name)
		assert.Equal(t, 0, c.Count)
		assert.NotNil(t, c.Items, "items must be an empty array, never null")
		assert.Empty(t, c.Items)
	}
	assert.Equal(t, []string{"open", "in_progress", "blocked", "deferred", "done", "cancelled", "invalidated"}, names)
}

// MTIX-255.1: grouping, counts independent of --limit, per-column limit, item fields.
func TestBoard_GroupsByStatus_CountIsTotalAndItemsAreCapped(t *testing.T) {
	b := &boardStore{byStatus: map[model.Status][]*model.Node{
		model.StatusOpen:       {boardNode("T-1", "a", 2, ""), boardNode("T-2", "b", 1, "bob"), boardNode("T-3", "c", 3, "")},
		model.StatusInProgress: {boardNode("T-4", "d", 2, "alice")},
	}}
	got, err := newBoardSvc(b).Board(context.Background(), service.BoardRequest{Limit: 2})
	require.NoError(t, err)
	open, prog := got.Columns[0], got.Columns[1]
	assert.Equal(t, 3, open.Count)
	require.Len(t, open.Items, 2)
	assert.Equal(t, service.BoardItem{ID: "T-1", Title: "a", Priority: 2}, open.Items[0])
	assert.Equal(t, service.BoardItem{ID: "T-2", Title: "b", Priority: 1, Assignee: "bob"}, open.Items[1])
	assert.Equal(t, 1, prog.Count)
	assert.Equal(t, "alice", prog.Items[0].Assignee)
	for _, o := range b.opts {
		assert.Equal(t, 2, o.Limit)
	}
}

// MTIX-255.1: the caller's filter reaches every per-column ListNodes call and is not mutated.
func TestBoard_Filter_PassedToEveryColumnQuery(t *testing.T) {
	b := &boardStore{}
	f := service.NodeFilter{Under: []string{"T-1"}, LabelsAny: []string{"desk:now"}, Assignee: []string{"bob"}, Project: "T"}
	_, err := newBoardSvc(b).Board(context.Background(), service.BoardRequest{Filter: f, Limit: 3})
	require.NoError(t, err)
	require.Len(t, b.calls, 7)
	seen := map[model.Status]bool{}
	for _, c := range b.calls {
		require.Len(t, c.Status, 1)
		seen[c.Status[0]] = true
		assert.Equal(t, f.Under, c.Under)
		assert.Equal(t, f.LabelsAny, c.LabelsAny)
		assert.Equal(t, f.Assignee, c.Assignee)
		assert.Equal(t, "T", c.Project)
	}
	assert.Len(t, seen, 7)
	assert.Empty(t, f.Status, "caller filter must not be mutated")
}

func TestBoard_InvalidRequest_ReturnsInvalidInput(t *testing.T) {
	for name, req := range map[string]service.BoardRequest{
		"zero limit":     {Limit: 0},
		"negative limit": {Limit: -1},
		"status filter":  {Limit: 1, Filter: service.NodeFilter{Status: []model.Status{model.StatusOpen}}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newBoardSvc(&boardStore{}).Board(context.Background(), req)
			assert.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

func TestBoard_StoreError_IsWrapped(t *testing.T) {
	sentinel := errors.New("boom")
	_, err := newBoardSvc(&boardStore{err: sentinel}).Board(context.Background(), service.BoardRequest{Limit: 1})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

// MTIX-255.1: the JSON shape is pinned.
func TestBoard_JSONShape_IsStable(t *testing.T) {
	b := &boardStore{byStatus: map[model.Status][]*model.Node{
		model.StatusOpen: {boardNode("T-1", "a", 2, "")},
	}}
	got, err := newBoardSvc(b).Board(context.Background(), service.BoardRequest{Limit: 5})
	require.NoError(t, err)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	want := `{"columns":[` +
		`{"name":"open","count":1,"items":[{"id":"T-1","title":"a","priority":2,"assignee":""}]},` +
		`{"name":"in_progress","count":0,"items":[]},` +
		`{"name":"blocked","count":0,"items":[]},` +
		`{"name":"deferred","count":0,"items":[]},` +
		`{"name":"done","count":0,"items":[]},` +
		`{"name":"cancelled","count":0,"items":[]},` +
		`{"name":"invalidated","count":0,"items":[]}]}`
	assert.JSONEq(t, want, string(raw))
}

// MTIX-255.1: the board never drops a status the model defines.
func TestBoard_Columns_CoverEveryModelStatus(t *testing.T) {
	got, err := newBoardSvc(&boardStore{}).Board(context.Background(), service.BoardRequest{Limit: 1})
	require.NoError(t, err)
	var names []string
	for _, c := range got.Columns {
		names = append(names, c.Name)
	}
	var want []string
	for _, s := range model.AllStatuses() {
		want = append(want, string(s))
	}
	assert.ElementsMatch(t, want, names)
}
