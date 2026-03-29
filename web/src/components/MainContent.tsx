/**
 * Main content area per FR-UI-2.
 * Renders the active view based on navigation context.
 */

import { useCallback, useEffect, useState } from "react";
import { useNavigation } from "../contexts/NavigationContext";
import { NodeDetailView } from "./NodeDetailView";
import { NodeListView } from "./NodeListView";
import { DashboardView } from "./DashboardView";
import { KanbanView } from "./KanbanView";
import type { useNodeStore } from "../hooks/useNodeStore";
import * as api from "../api";

interface MainContentProps {
  nodeStore: ReturnType<typeof useNodeStore>;
}

export function MainContent({ nodeStore }: MainContentProps) {
  const { view, selectedNodeId, selectNode } = useNavigation();

  return (
    <main
      className="h-full"
      style={{ backgroundColor: "var(--color-bg)" }}
    >
      {view === "node-detail" && selectedNodeId ? (
        <NodeDetailView
          nodeId={selectedNodeId}
          nodeStore={nodeStore}
          onNavigate={selectNode}
        />
      ) : view === "dashboard" ? (
        <DashboardView onNavigate={selectNode} />
      ) : view === "kanban" ? (
        <KanbanView nodeStore={nodeStore} />
      ) : view === "stale" ? (
        <StaleView onNavigate={selectNode} />
      ) : view === "agents" ? (
        <AgentsView onNavigate={selectNode} />
      ) : (
        <NodeListView onSelectNode={selectNode} />
      )}
    </main>
  );
}

/** Stale board view — fetches stale agent IDs from GET /stale. */
function StaleView(_props: { onNavigate: (nodeId: string) => void }) {
  const [staleAgents, setStaleAgents] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    api.getStaleEntries()
      .then((data) => setStaleAgents(data.agents ?? []))
      .catch(() => setStaleAgents([]))
      .finally(() => setLoading(false));
  }, []);

  return (
    <div className="p-6" data-testid="stale-view">
      <h2
        className="text-lg font-medium mb-4"
        style={{ color: "var(--color-text-primary)" }}
      >
        Stale Board
      </h2>

      {loading ? (
        <p className="text-sm" style={{ color: "var(--color-text-secondary)" }}>
          Loading stale entries...
        </p>
      ) : staleAgents.length === 0 ? (
        <div className="text-center py-8">
          <p className="text-sm" style={{ color: "var(--color-text-secondary)" }}>
            No stale items
          </p>
          <p className="text-xs mt-1" style={{ color: "var(--color-text-tertiary)" }}>
            Everything is running smoothly
          </p>
        </div>
      ) : (
        <div className="space-y-2">
          <p className="text-xs mb-3" style={{ color: "var(--color-text-secondary)" }}>
            {staleAgents.length} stale agent{staleAgents.length !== 1 ? "s" : ""} detected
          </p>
          {staleAgents.map((agentId) => (
            <div
              key={agentId}
              className="flex items-center gap-3 px-3 py-2 rounded border"
              style={{
                borderColor: "var(--color-status-blocked)",
                backgroundColor: "var(--color-surface)",
              }}
            >
              <span
                className="w-2 h-2 rounded-full shrink-0"
                style={{ backgroundColor: "var(--color-status-blocked)" }}
              />
              <span
                className="text-sm font-medium"
                style={{ color: "var(--color-text-primary)" }}
              >
                Agent: {agentId}
              </span>
              <span
                className="text-xs px-1.5 py-0.5 rounded"
                style={{
                  backgroundColor: "var(--color-status-blocked)",
                  color: "#FFFFFF",
                }}
              >
                Stale heartbeat
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

/** Agent activity view — fetches agents with in_progress nodes. */
function AgentsView({ onNavigate }: { onNavigate: (nodeId: string) => void }) {
  const [agents, setAgents] = useState<{ id: string; node_id: string; node_title: string }[]>([]);
  const [loading, setLoading] = useState(true);

  const loadAgents = useCallback(async () => {
    setLoading(true);
    try {
      // Fetch in_progress nodes to show active agent work.
      const result = await api.listNodes({ status: "in_progress", limit: 50 });
      const nodes = result.nodes ?? [];
      const agentMap = new Map<string, { id: string; node_id: string; node_title: string }>();
      for (const node of nodes) {
        if (node.assignee) {
          agentMap.set(node.assignee, {
            id: node.assignee,
            node_id: node.id,
            node_title: node.title,
          });
        }
      }
      setAgents(Array.from(agentMap.values()));
    } catch {
      setAgents([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadAgents();
  }, [loadAgents]);

  return (
    <div className="p-6" data-testid="agents-view">
      <h2
        className="text-lg font-medium mb-4"
        style={{ color: "var(--color-text-primary)" }}
      >
        Agent Activity
      </h2>

      {loading ? (
        <p className="text-sm" style={{ color: "var(--color-text-secondary)" }}>
          Loading agent activity...
        </p>
      ) : agents.length === 0 ? (
        <div className="text-center py-8">
          <p className="text-sm" style={{ color: "var(--color-text-secondary)" }}>
            No active agents
          </p>
          <p className="text-xs mt-1" style={{ color: "var(--color-text-tertiary)" }}>
            Agents working on tasks will appear here
          </p>
        </div>
      ) : (
        <div className="space-y-3">
          <p className="text-xs mb-3" style={{ color: "var(--color-text-secondary)" }}>
            {agents.length} active agent{agents.length !== 1 ? "s" : ""}
          </p>
          {agents.map((agent) => (
            <div
              key={agent.id}
              className="rounded border p-4"
              style={{
                borderColor: "var(--color-border)",
                backgroundColor: "var(--color-surface)",
              }}
            >
              <div className="flex items-center gap-2 mb-2">
                <span
                  className="text-sm font-medium"
                  style={{ color: "var(--color-accent)" }}
                >
                  {agent.id}
                </span>
                <span
                  className="text-xs px-1.5 py-0.5 rounded font-bold uppercase"
                  style={{
                    backgroundColor: "var(--color-status-in-progress)",
                    color: "#FFFFFF",
                  }}
                >
                  working
                </span>
              </div>
              <div className="flex items-center gap-2">
                <span className="text-xs" style={{ color: "var(--color-text-secondary)" }}>
                  Working on:
                </span>
                <button
                  className="text-sm cursor-pointer hover:underline"
                  style={{ color: "var(--color-text-primary)" }}
                  onClick={() => onNavigate(agent.node_id)}
                >
                  {agent.node_id} &quot;{agent.node_title}&quot;
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
