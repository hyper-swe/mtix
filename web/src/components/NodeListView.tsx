/**
 * Node list view — shows all nodes with status filtering.
 * Default view when no node is selected.
 */

import { useCallback, useEffect, useState } from "react";
import type { Node } from "../types";
import { StatusIcon } from "./StatusIcon";
import * as api from "../api";

interface NodeListViewProps {
  onSelectNode: (nodeId: string) => void;
}

const STATUSES = [
  { key: "", label: "All" },
  { key: "open", label: "Open" },
  { key: "in_progress", label: "In Progress" },
  { key: "blocked", label: "Blocked" },
  { key: "done", label: "Done" },
  { key: "deferred", label: "Deferred" },
  { key: "cancelled", label: "Cancelled" },
];

export function NodeListView({ onSelectNode }: NodeListViewProps) {
  const [nodes, setNodes] = useState<Node[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState<string>("");
  const [hoveredId, setHoveredId] = useState<string | null>(null);

  const loadNodes = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const result = await api.listNodes({
        status: statusFilter || undefined,
        limit: 100,
      });
      setNodes(result.nodes ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load nodes");
      setNodes([]);
    } finally {
      setLoading(false);
    }
  }, [statusFilter]);

  useEffect(() => {
    loadNodes();
  }, [loadNodes]);

  // j/k navigation
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement)?.tagName;
      if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
      if (nodes.length === 0) return;

      if (e.key === "j" || e.key === "k") {
        e.preventDefault();
        const currentIdx = hoveredId ? nodes.findIndex((n) => n.id === hoveredId) : -1;
        const nextIdx = e.key === "j"
          ? Math.min(currentIdx + 1, nodes.length - 1)
          : Math.max(currentIdx - 1, 0);
        const node = nodes[nextIdx];
        if (node) setHoveredId(node.id);
      }
      if (e.key === "Enter" && hoveredId) {
        e.preventDefault();
        onSelectNode(hoveredId);
      }
      // x — mark done shortcut
      if (e.key === "x" && hoveredId) {
        e.preventDefault();
        api.transitionNode(hoveredId, "done", {}).then(() => loadNodes()).catch(() => {});
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [nodes, hoveredId, onSelectNode, loadNodes]);

  return (
    <div className="h-full flex flex-col">
      {/* Header with filters */}
      <div
        className="flex items-center gap-3 px-4 py-2.5"
        style={{ borderBottom: "1px solid var(--color-border)" }}
      >
        <h2
          className="text-sm font-semibold"
          style={{ color: "var(--color-text-primary)" }}
        >
          Issues
        </h2>

        <div className="flex gap-0.5 ml-auto">
          {STATUSES.map((s) => (
            <button
              key={s.key || "all"}
              className="px-2 py-1 text-xs rounded cursor-pointer"
              style={{
                backgroundColor: statusFilter === s.key ? "var(--color-accent-muted)" : "transparent",
                color: statusFilter === s.key ? "var(--color-accent)" : "var(--color-text-tertiary)",
                borderRadius: "var(--radius-sm)",
                fontWeight: statusFilter === s.key ? 500 : 400,
              }}
              onMouseEnter={(e) => {
                if (statusFilter !== s.key) e.currentTarget.style.backgroundColor = "var(--color-hover)";
              }}
              onMouseLeave={(e) => {
                if (statusFilter !== s.key) e.currentTarget.style.backgroundColor = "transparent";
              }}
              onClick={() => setStatusFilter(s.key)}
            >
              {s.label}
            </button>
          ))}
        </div>
      </div>

      {/* Node list */}
      <div className="flex-1 overflow-y-auto">
        {loading ? (
          <div
            className="flex items-center justify-center h-32"
            style={{ color: "var(--color-text-tertiary)" }}
          >
            <span className="text-sm">Loading...</span>
          </div>
        ) : error ? (
          <div className="flex flex-col items-center justify-center h-32 gap-2">
            <span className="text-sm" style={{ color: "var(--color-status-blocked)" }}>
              {error}
            </span>
            <button
              className="text-xs cursor-pointer px-3 py-1 rounded"
              style={{
                color: "var(--color-accent)",
                border: "1px solid var(--color-border)",
                borderRadius: "var(--radius-md)",
              }}
              onClick={loadNodes}
            >
              Retry
            </button>
          </div>
        ) : nodes.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-48 gap-2">
            <div
              className="w-10 h-10 rounded-full flex items-center justify-center mb-2"
              style={{ backgroundColor: "var(--color-hover)" }}
            >
              <svg width="20" height="20" viewBox="0 0 20 20" fill="none" stroke="var(--color-text-tertiary)" strokeWidth="1.5" strokeLinecap="round">
                <circle cx="10" cy="10" r="8" />
                <line x1="10" y1="6" x2="10" y2="10" />
                <line x1="10" y1="10" x2="13" y2="12" />
              </svg>
            </div>
            <span className="text-sm" style={{ color: "var(--color-text-secondary)" }}>
              No issues found
            </span>
            <span className="text-xs" style={{ color: "var(--color-text-tertiary)" }}>
              Press <kbd>c</kbd> to create or use the CLI
            </span>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr
                className="text-[11px] text-left"
                style={{
                  borderBottom: "1px solid var(--color-border)",
                  color: "var(--color-text-tertiary)",
                }}
              >
                <th className="px-4 py-2 font-medium w-8"></th>
                <th className="px-2 py-2 font-medium">ID</th>
                <th className="px-2 py-2 font-medium">Title</th>
                <th className="px-2 py-2 font-medium w-16 text-center">Priority</th>
                <th className="px-2 py-2 font-medium w-24">Assignee</th>
                <th className="px-4 py-2 font-medium w-32">Progress</th>
              </tr>
            </thead>
            <tbody>
              {nodes.map((node) => (
                <tr
                  key={node.id}
                  className="cursor-pointer"
                  style={{
                    borderBottom: "1px solid var(--color-border-subtle)",
                    backgroundColor: hoveredId === node.id ? "var(--color-hover)" : "transparent",
                    height: "var(--row-height)",
                  }}
                  onClick={() => onSelectNode(node.id)}
                  onMouseEnter={() => setHoveredId(node.id)}
                  onMouseLeave={() => setHoveredId(null)}
                >
                  <td className="px-4 py-1.5">
                    <StatusIcon status={node.status} size={14} />
                  </td>
                  <td className="px-2 py-1.5">
                    <span
                      className="font-mono text-xs"
                      style={{ color: "var(--color-text-tertiary)" }}
                    >
                      {node.id}
                    </span>
                  </td>
                  <td className="px-2 py-1.5">
                    <span
                      className="text-sm"
                      style={{
                        color: "var(--color-text-primary)",
                        textDecoration:
                          node.status === "done" || node.status === "cancelled"
                            ? "line-through"
                            : "none",
                        opacity:
                          node.status === "done" || node.status === "cancelled"
                            ? 0.6
                            : 1,
                      }}
                    >
                      {node.title}
                    </span>
                  </td>
                  <td className="px-2 py-1.5 text-center">
                    <PriorityIndicator priority={node.priority} />
                  </td>
                  <td className="px-2 py-1.5">
                    <span
                      className="text-xs truncate block max-w-[80px]"
                      style={{ color: "var(--color-text-tertiary)" }}
                    >
                      {node.assignee || "\u2014"}
                    </span>
                  </td>
                  <td className="px-4 py-1.5">
                    <div className="flex items-center gap-2">
                      <div
                        className="flex-1 h-1 rounded-full overflow-hidden"
                        style={{ backgroundColor: "var(--color-border)" }}
                      >
                        <div
                          className="h-full rounded-full"
                          style={{
                            width: `${Math.round(node.progress * 100)}%`,
                            backgroundColor: "var(--color-status-done)",
                            transition: "width 300ms ease",
                          }}
                        />
                      </div>
                      <span
                        className="text-[10px] tabular-nums w-7 text-right"
                        style={{ color: "var(--color-text-tertiary)" }}
                      >
                        {Math.round(node.progress * 100)}%
                      </span>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function PriorityIndicator({ priority }: { priority: number }) {
  const colors: Record<number, string> = {
    0: "var(--color-status-blocked)",
    1: "var(--color-status-invalidated)",
    2: "var(--color-text-tertiary)",
    3: "var(--color-text-tertiary)",
    4: "var(--color-text-tertiary)",
  };
  const labels: Record<number, string> = {
    0: "Urgent",
    1: "High",
    2: "Med",
    3: "Low",
    4: "None",
  };

  return (
    <span
      className="text-[10px] font-medium"
      style={{ color: colors[priority] ?? "var(--color-text-tertiary)" }}
    >
      {labels[priority] ?? `P${priority}`}
    </span>
  );
}
