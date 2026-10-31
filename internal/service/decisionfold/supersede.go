// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold

import (
	"slices"

	"github.com/hyper-swe/mtix/internal/model"
)

// supersedeGraph holds the edges "newer request supersedes older request"
// among requests that are not invalid, and their strongly connected
// components.
type supersedeGraph struct {
	// out maps a superseding request to the requests it supersedes.
	out map[string][]string
	// in maps a request to the requests that supersede it.
	in map[string][]string
	// comp maps a request to its component number.
	comp map[string]int
	// size maps a component number to its member count.
	size map[int]int
}

// supersedes reports whether newer may supersede older: same author, and
// the same key, or no key on both sides and the same ticket.
func (f *folder) supersedes(newer, older Event) bool {
	if newer.EventID == older.EventID || newer.AuthorID != older.AuthorID {
		return false
	}
	if older.Class != model.DecisionValid || older.Kind != model.DecisionKindRequest || older.Decision == nil || older.Decision.Request == nil {
		return false
	}
	n, o := newer.Decision.Request, older.Decision.Request
	if n.Key != "" || o.Key != "" {
		return n.Key == o.Key
	}
	return n.Ticket.UID == o.Ticket.UID
}

func (f *folder) buildGraph() *supersedeGraph {
	g := &supersedeGraph{
		out: map[string][]string{}, in: map[string][]string{},
		comp: map[string]int{}, size: map[int]int{},
	}
	ids := f.requestIDs()
	for _, id := range ids {
		if f.reason[id] != ReasonNone {
			continue
		}
		newer := f.idx.events[id]
		for _, ref := range newer.Decision.Request.Supersedes {
			target := f.idx.resolve(ref)
			older, ok := f.idx.events[target]
			if !ok || !f.supersedes(newer, older) {
				continue
			}
			if !slices.Contains(g.out[id], target) {
				g.out[id] = append(g.out[id], target)
				g.in[target] = append(g.in[target], id)
			}
		}
	}
	g.components(ids)
	return g
}

// components assigns strongly connected components with Tarjan's algorithm.
func (g *supersedeGraph) components(ids []string) {
	index, next := map[string]int{}, 0
	low := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var visit func(v string)
	visit = func(v string) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range g.out[v] {
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		c := len(g.size)
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			g.comp[w] = c
			g.size[c]++
			if w == v {
				return
			}
		}
	}
	for _, id := range ids {
		if _, seen := index[id]; !seen {
			visit(id)
		}
	}
}

// inCycle reports whether id belongs to a component of more than one request.
func (g *supersedeGraph) inCycle(id string) bool {
	c, ok := g.comp[id]
	return ok && g.size[c] > 1
}

// supersededBy returns the requests outside id's component that supersede it.
func (g *supersedeGraph) supersededBy(id string) []string {
	var out []string
	for _, src := range g.in[id] {
		if g.comp[src] != g.comp[id] {
			out = append(out, src)
		}
	}
	slices.Sort(out)
	return out
}
