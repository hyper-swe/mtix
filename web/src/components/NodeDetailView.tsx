/**
 * Node detail view — fetches and displays a single node.
 * Wraps NodeDetail with API data fetching.
 */

import { useCallback, useEffect, useState } from "react";
import type { Node, ContextEntry, ActivityEntry, Dependency } from "../types";
import { NodeDetail } from "./NodeDetail";
import * as api from "../api";
import type { useNodeStore } from "../hooks/useNodeStore";

interface NodeDetailViewProps {
  nodeId: string;
  nodeStore: ReturnType<typeof useNodeStore>;
  onNavigate: (nodeId: string) => void;
}

export function NodeDetailView({ nodeId, nodeStore, onNavigate }: NodeDetailViewProps) {
  const [node, setNode] = useState<Node | null>(null);
  const [children, setChildren] = useState<Node[]>([]);
  const [contextChain] = useState<ContextEntry[]>([]);
  const [activityEntries, setActivityEntries] = useState<ActivityEntry[]>([]);
  const [activityHasMore, setActivityHasMore] = useState(false);
  const [activityLoading, setActivityLoading] = useState(false);
  const [dependencies, setDependencies] = useState<Dependency[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const loadNode = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [nodeData, childData, activityData, depsData] = await Promise.all([
        api.getNode(nodeId),
        api.getChildren(nodeId).catch(() => []),
        api.getActivity(nodeId, { limit: 20 }).catch(() => []),
        api.getDependencies(nodeId).catch(() => []),
      ]);
      setNode(nodeData);
      setChildren(childData);
      setActivityEntries(activityData);
      setActivityHasMore(activityData.length >= 20);
      setDependencies(depsData);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load node");
    } finally {
      setLoading(false);
    }
  }, [nodeId]);

  const loadMoreActivity = useCallback(async () => {
    setActivityLoading(true);
    try {
      const more = await api.getActivity(nodeId, { limit: 20, offset: activityEntries.length });
      setActivityEntries((prev) => [...prev, ...more]);
      setActivityHasMore(more.length >= 20);
    } catch {
      // Silently fail.
    } finally {
      setActivityLoading(false);
    }
  }, [nodeId, activityEntries.length]);

  useEffect(() => {
    loadNode();
  }, [loadNode]);

  const handleStatusChange = useCallback(
    async (status: string) => {
      if (!node) return;
      // Map status to transition action.
      const actionMap: Record<string, string> = {
        in_progress: "claim",
        done: "done",
        deferred: "defer",
        cancelled: "cancel",
        open: "reopen",
      };
      const action = actionMap[status];
      if (!action) return;

      try {
        const body: Record<string, unknown> = {};
        if (action === "cancel") body.reason = "Cancelled via UI";
        if (action === "claim") body.agent = "web-user";
        await api.transitionNode(nodeId, action, body);
        await loadNode();
      } catch {
        // Silently fail — could show toast.
      }
    },
    [node, nodeId, loadNode],
  );

  const handleUpdateTitle = useCallback(
    async (title: string) => {
      try {
        const updated = await api.updateNode(nodeId, { title } as Partial<Node>);
        setNode(updated);
        nodeStore.updateNode(nodeId, { title });
      } catch {
        // Silently fail.
      }
    },
    [nodeId, nodeStore],
  );

  const handleSavePrompt = useCallback(
    async (prompt: string) => {
      try {
        const updated = await api.updatePrompt(nodeId, prompt);
        setNode(updated);
      } catch {
        // Silently fail.
      }
    },
    [nodeId],
  );

  const handleSaveAndRerun = useCallback(
    async (prompt: string, strategy: api.RerunStrategy) => {
      try {
        await api.updatePrompt(nodeId, prompt);
        await api.rerunChildren(nodeId, strategy);
        await loadNode();
      } catch {
        // Silently fail.
      }
    },
    [nodeId, loadNode],
  );

  const handleAddAnnotation = useCallback(
    async (text: string) => {
      try {
        await api.addAnnotation(nodeId, text);
        await loadNode();
      } catch {
        // Silently fail.
      }
    },
    [nodeId, loadNode],
  );

  const handleCreateChild = useCallback(
    async (title: string, prompt?: string) => {
      try {
        const child = await api.createNode({
          title,
          parent_id: nodeId,
          prompt: prompt ?? "",
        });
        nodeStore.addNode(child);
        setChildren((prev) => [...prev, child]);
      } catch {
        // Silently fail.
      }
    },
    [nodeId, nodeStore],
  );

  if (loading) {
    return (
      <div
        className="flex items-center justify-center h-full"
        style={{ color: "var(--color-text-secondary)" }}
      >
        <span className="text-sm">Loading node...</span>
      </div>
    );
  }

  if (error || !node) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-center">
          <span className="text-sm" style={{ color: "var(--color-status-blocked)" }}>
            {error ?? "Node not found"}
          </span>
          <button
            className="block mt-2 text-xs cursor-pointer"
            style={{ color: "var(--color-accent)" }}
            onClick={() => onNavigate("")}
          >
            Go back
          </button>
        </div>
      </div>
    );
  }

  return (
    <NodeDetail
      node={node}
      children={children}
      contextChain={contextChain}
      activityEntries={activityEntries}
      activityHasMore={activityHasMore}
      onLoadMoreActivity={loadMoreActivity}
      activityLoading={activityLoading}
      dependencies={dependencies}
      onUpdateTitle={handleUpdateTitle}
      onStatusChange={handleStatusChange}
      onSavePrompt={handleSavePrompt}
      onSaveAndRerun={handleSaveAndRerun}
      onAddAnnotation={handleAddAnnotation}
      onResolveAnnotation={() => {}}
      onNavigate={onNavigate}
      onCreateChild={handleCreateChild}
    />
  );
}
