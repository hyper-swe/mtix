/**
 * Client-side node store using React state.
 * Manages the tree of nodes for the sidebar and main content.
 * No external state library — pure React hooks.
 */

import { useCallback, useRef, useState } from "react";
import type { Node, Status } from "../types";
import * as api from "../api";

/**
 * Status sort order for tree view per MTIX-16.1.
 * Active work first, then actionable, then terminal.
 */
const STATUS_SORT_ORDER: Record<Status, number> = {
  in_progress: 0,
  open: 1,
  blocked: 2,
  deferred: 3,
  done: 4,
  cancelled: 5,
  invalidated: 6,
};

/** Compare two nodes by status priority, then by seq for stability. */
function compareByStatus(a: Node, b: Node): number {
  const statusDiff = (STATUS_SORT_ORDER[a.status] ?? 9) - (STATUS_SORT_ORDER[b.status] ?? 9);
  if (statusDiff !== 0) return statusDiff;
  return a.seq - b.seq;
}

interface NodeStoreState {
  /** All loaded nodes keyed by ID. */
  nodes: Map<string, Node>;
  /** IDs of expanded tree nodes. */
  expanded: Set<string>;
  /** Currently selected node ID. */
  selectedId: string | null;
  /** Root-level node IDs (depth 0). */
  rootIds: string[];
  /** Children IDs keyed by parent ID. */
  childrenMap: Map<string, string[]>;
  /** Set of parent IDs whose children have been loaded. */
  loadedChildren: Set<string>;
  /** Set of parent IDs currently loading children. */
  loadingChildren: Set<string>;
  /** Loading state. */
  loading: boolean;
}

/** Flattened tree item for rendering. */
export interface TreeItem {
  node: Node;
  depth: number;
  isExpanded: boolean;
  hasChildren: boolean;
  isLoadingChildren: boolean;
}

export function useNodeStore() {
  const [state, setState] = useState<NodeStoreState>({
    nodes: new Map(),
    expanded: new Set(),
    selectedId: null,
    rootIds: [],
    childrenMap: new Map(),
    loadedChildren: new Set(),
    loadingChildren: new Set(),
    loading: false,
  });

  /** Whether to hide completed (done/cancelled) nodes in tree per MTIX-16.2. */
  const [hideDone, setHideDone] = useState(false);

  // Use ref to track in-flight fetches to prevent duplicates.
  const fetchingRef = useRef<Set<string>>(new Set());
  // Use ref for loadedChildren to avoid stale closures.
  const loadedChildrenRef = useRef<Set<string>>(new Set());

  /** Load root-level nodes via /orphans endpoint. */
  const loadRoots = useCallback(async () => {
    setState((prev) => ({ ...prev, loading: true }));
    try {
      const result = await api.getRootNodes(200);
      const nodes = result.nodes ?? [];
      setState((prev) => {
        const newNodes = new Map(prev.nodes);
        const rootIds: string[] = [];
        for (const node of nodes) {
          newNodes.set(node.id, node);
          rootIds.push(node.id);
        }
        return { ...prev, nodes: newNodes, rootIds, loading: false };
      });
    } catch {
      setState((prev) => ({ ...prev, loading: false }));
    }
  }, []);

  /** Load children of a node via /nodes/:id/children. */
  const loadChildren = useCallback(async (parentId: string) => {
    if (fetchingRef.current.has(parentId)) return;
    fetchingRef.current.add(parentId);

    setState((prev) => {
      const newLoading = new Set(prev.loadingChildren);
      newLoading.add(parentId);
      return { ...prev, loadingChildren: newLoading };
    });

    try {
      const children = await api.getChildren(parentId);
      setState((prev) => {
        const newNodes = new Map(prev.nodes);
        const childIds: string[] = [];
        for (const child of children) {
          newNodes.set(child.id, child);
          childIds.push(child.id);
        }
        const newChildrenMap = new Map(prev.childrenMap);
        newChildrenMap.set(parentId, childIds);
        const newLoaded = new Set(prev.loadedChildren);
        newLoaded.add(parentId);
        loadedChildrenRef.current = newLoaded;
        const newLoading = new Set(prev.loadingChildren);
        newLoading.delete(parentId);
        return {
          ...prev,
          nodes: newNodes,
          childrenMap: newChildrenMap,
          loadedChildren: newLoaded,
          loadingChildren: newLoading,
        };
      });
    } catch {
      setState((prev) => {
        const newLoading = new Set(prev.loadingChildren);
        newLoading.delete(parentId);
        // Mark as loaded even on error so we don't retry endlessly.
        const newLoaded = new Set(prev.loadedChildren);
        newLoaded.add(parentId);
        loadedChildrenRef.current = newLoaded;
        return { ...prev, loadingChildren: newLoading, loadedChildren: newLoaded };
      });
    } finally {
      fetchingRef.current.delete(parentId);
    }
  }, []);

  /** Toggle expanded state of a node. Lazy-loads children if needed. */
  const toggleExpand = useCallback(
    async (nodeId: string) => {
      let shouldLoad = false;

      setState((prev) => {
        const newExpanded = new Set(prev.expanded);
        if (newExpanded.has(nodeId)) {
          newExpanded.delete(nodeId);
        } else {
          newExpanded.add(nodeId);
          // Check loadedChildren from current state, not stale closure.
          if (!prev.loadedChildren.has(nodeId)) {
            shouldLoad = true;
          }
        }
        return { ...prev, expanded: newExpanded };
      });

      if (shouldLoad) {
        await loadChildren(nodeId);
      }
    },
    [loadChildren],
  );

  /** Select a node. */
  const selectNode = useCallback((nodeId: string | null) => {
    setState((prev) => ({ ...prev, selectedId: nodeId }));
  }, []);

  /** Update a node in the store (from WebSocket events). */
  const updateNode = useCallback((nodeId: string, fields: Partial<Node>) => {
    setState((prev) => {
      const existing = prev.nodes.get(nodeId);
      if (!existing) return prev;
      const newNodes = new Map(prev.nodes);
      newNodes.set(nodeId, { ...existing, ...fields });
      return { ...prev, nodes: newNodes };
    });
  }, []);

  /** Add a new node to the store. */
  const addNode = useCallback((node: Node) => {
    setState((prev) => {
      const newNodes = new Map(prev.nodes);
      newNodes.set(node.id, node);

      let newRootIds = prev.rootIds;
      if (node.depth === 0 && !prev.rootIds.includes(node.id)) {
        newRootIds = [...prev.rootIds, node.id];
      }

      const newChildrenMap = new Map(prev.childrenMap);
      if (node.parent_id) {
        const siblings = newChildrenMap.get(node.parent_id) ?? [];
        if (!siblings.includes(node.id)) {
          newChildrenMap.set(node.parent_id, [...siblings, node.id]);
        }
      }

      return { ...prev, nodes: newNodes, rootIds: newRootIds, childrenMap: newChildrenMap };
    });
  }, []);

  /** Remove a node from the store. */
  const removeNode = useCallback((nodeId: string) => {
    setState((prev) => {
      const newNodes = new Map(prev.nodes);
      const removed = newNodes.get(nodeId);
      newNodes.delete(nodeId);

      const newRootIds = prev.rootIds.filter((id) => id !== nodeId);
      const newChildrenMap = new Map(prev.childrenMap);
      if (removed?.parent_id) {
        const siblings = newChildrenMap.get(removed.parent_id);
        if (siblings) {
          newChildrenMap.set(
            removed.parent_id,
            siblings.filter((id) => id !== nodeId),
          );
        }
      }
      newChildrenMap.delete(nodeId);

      return { ...prev, nodes: newNodes, rootIds: newRootIds, childrenMap: newChildrenMap };
    });
  }, []);

  /** Build flattened tree for rendering, sorted by status per MTIX-16.1. */
  const flatTree = useCallback((): TreeItem[] => {
    const items: TreeItem[] = [];
    const isTerminal = (s: Status) => s === "done" || s === "cancelled";

    /** Resolve IDs to nodes, sort by status, optionally filter done. */
    function sortedNodes(ids: string[]): Node[] {
      const nodes: Node[] = [];
      for (const id of ids) {
        const node = state.nodes.get(id);
        if (!node) continue;
        if (hideDone && isTerminal(node.status)) continue;
        nodes.push(node);
      }
      return nodes.sort(compareByStatus);
    }

    function walk(ids: string[], depth: number) {
      const sorted = sortedNodes(ids);
      for (const node of sorted) {
        const childIds = state.childrenMap.get(node.id) ?? [];
        const isExpanded = state.expanded.has(node.id);
        const isLoaded = state.loadedChildren.has(node.id);
        const isLoading = state.loadingChildren.has(node.id);

        // A node has children if:
        // - child_count > 0 from API (if populated), OR
        // - we've loaded children and found some, OR
        // - it's not yet loaded (assume it might have children — show chevron)
        const hasChildren =
          node.child_count > 0 ||
          childIds.length > 0 ||
          (!isLoaded && node.depth < 10); // assume expandable until proven otherwise

        items.push({ node, depth, isExpanded, hasChildren, isLoadingChildren: isLoading });

        if (isExpanded && childIds.length > 0) {
          walk(childIds, depth + 1);
        }
      }
    }

    walk(state.rootIds, 0);
    return items;
  }, [state.nodes, state.rootIds, state.expanded, state.childrenMap, state.loadedChildren, state.loadingChildren, hideDone]);

  return {
    ...state,
    hideDone,
    setHideDone,
    loadRoots,
    loadChildren,
    toggleExpand,
    selectNode,
    updateNode,
    addNode,
    removeNode,
    flatTree,
  };
}
