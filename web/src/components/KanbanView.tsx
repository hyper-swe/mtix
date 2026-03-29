/**
 * Kanban board view per FR-UI-21.
 * Displays nodes in status columns with drag-and-drop transitions,
 * keyboard navigation, and real-time WebSocket updates.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import * as api from "../api";
import { useWebSocket } from "../contexts/WebSocketContext";
import { NodeDetailView } from "./NodeDetailView";
import type { useNodeStore } from "../hooks/useNodeStore";
import type { Node, Status } from "../types/node";

/** Fixed column order per FR-UI-21n. */
const COLUMN_ORDER: Status[] = [
  "open",
  "in_progress",
  "blocked",
  "deferred",
  "done",
  "cancelled",
];

/** Human-readable column labels. */
const COLUMN_LABELS: Record<string, string> = {
  open: "Open",
  in_progress: "In Progress",
  blocked: "Blocked",
  deferred: "Deferred",
  done: "Done",
  cancelled: "Cancelled",
};

/** Status color CSS variable names for column accent bars. */
const STATUS_COLORS: Record<string, string> = {
  open: "var(--color-status-open)",
  in_progress: "var(--color-status-in-progress)",
  blocked: "var(--color-status-blocked)",
  deferred: "var(--color-status-deferred)",
  done: "var(--color-status-done)",
  cancelled: "var(--color-status-cancelled)",
};

/** Priority indicator colors. */
const PRIORITY_COLORS: Record<number, string> = {
  1: "#ef4444", // Critical — red
  2: "#f97316", // High — orange
  3: "#eab308", // Medium — yellow
  4: "#3b82f6", // Low — blue
  5: "#94a3b8", // Backlog — muted
};

interface KanbanViewProps {
  nodeStore: ReturnType<typeof useNodeStore>;
}

export function KanbanView({ nodeStore }: KanbanViewProps) {
  const [nodes, setNodes] = useState<Node[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [focusCol, setFocusCol] = useState(0);
  const [focusRow, setFocusRow] = useState(0);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const boardRef = useRef<HTMLDivElement>(null);
  const { subscribe } = useWebSocket();

  const loadNodes = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const result = await api.listNodes({ limit: 500 });
      setNodes(result.nodes ?? []);
    } catch {
      setError("Failed to load board data");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadNodes();
  }, [loadNodes]);

  // Real-time updates via WebSocket per FR-UI-21j.
  useEffect(() => {
    const unsubs = [
      subscribe("node.created" as Parameters<typeof subscribe>[0], () => loadNodes()),
      subscribe("node.updated" as Parameters<typeof subscribe>[0], () => loadNodes()),
      subscribe("node.deleted" as Parameters<typeof subscribe>[0], () => loadNodes()),
      subscribe("status.changed" as Parameters<typeof subscribe>[0], () => loadNodes()),
    ];
    return () => unsubs.forEach((fn) => fn());
  }, [subscribe, loadNodes]);

  // Group nodes by status column per FR-UI-21b.
  // Invalidated nodes are grouped with "open" per FR-UI-21b.
  const columnNodes = useMemo(() => {
    const grouped: Record<string, Node[]> = {};
    for (const col of COLUMN_ORDER) {
      grouped[col] = [];
    }
    for (const node of nodes) {
      if (node.status === "invalidated") {
        // Invalidated shown in open column with badge per FR-UI-21b.
        grouped["open"]!.push(node);
      } else {
        const col = grouped[node.status];
        if (col) col.push(node);
      }
    }
    return grouped;
  }, [nodes]);

  // Keyboard navigation per FR-UI-21k.
  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      const target = e.target as HTMLElement;
      if (target.tagName === "INPUT" || target.tagName === "TEXTAREA") return;

      const focusStatus = COLUMN_ORDER[focusCol] as Status;
      const colNodes = columnNodes[focusStatus] ?? [];

      switch (e.key) {
        case "Escape":
          e.preventDefault();
          setSelectedId(null);
          break;
        case "ArrowRight":
          e.preventDefault();
          setFocusCol((prev) => Math.min(prev + 1, COLUMN_ORDER.length - 1));
          setFocusRow(0);
          break;
        case "ArrowLeft":
          e.preventDefault();
          setFocusCol((prev) => Math.max(prev - 1, 0));
          setFocusRow(0);
          break;
        case "ArrowDown":
          e.preventDefault();
          setFocusRow((prev) => Math.min(prev + 1, colNodes.length - 1));
          break;
        case "ArrowUp":
          e.preventDefault();
          setFocusRow((prev) => Math.max(prev - 1, 0));
          break;
        case "Enter": {
          e.preventDefault();
          if (colNodes[focusRow]) {
            setSelectedId(colNodes[focusRow].id);
          }
          break;
        }
      }
    },
    [columnNodes, focusCol, focusRow],
  );

  const handleCardSelect = useCallback((nodeId: string) => {
    setSelectedId(nodeId);
  }, []);

  if (loading) {
    return (
      <div
        className="flex items-center justify-center h-full"
        data-testid="kanban-loading"
        style={{ color: "var(--color-text-secondary)" }}
      >
        <div className="text-sm">Loading board...</div>
      </div>
    );
  }

  if (error) {
    return (
      <div
        className="flex items-center justify-center h-full"
        style={{ color: "var(--color-status-blocked)" }}
      >
        <div className="text-sm">{error}</div>
      </div>
    );
  }

  return (
    <div className="h-full flex" style={{ backgroundColor: "var(--color-bg)" }}>
      {/* Board columns */}
      <div
        ref={boardRef}
        className="flex-1 min-w-0 flex gap-3 p-4 overflow-x-auto"
        data-testid="kanban-board"
        tabIndex={0}
        onKeyDown={handleKeyDown}
      >
        {COLUMN_ORDER.map((status, colIdx) => (
          <KanbanColumn
            key={status}
            status={status}
            nodes={columnNodes[status] ?? []}
            focusedCardIdx={colIdx === focusCol ? focusRow : -1}
            selectedId={selectedId}
            onSelectNode={handleCardSelect}
          />
        ))}
      </div>

      {/* Detail panel — reuses NodeDetailView, navigation stays in Kanban */}
      {selectedId && (
        <div
          className="h-full flex flex-col"
          style={{
            width: "50%",
            minWidth: "400px",
            borderLeft: "1px solid var(--color-border)",
          }}
          data-testid="kanban-detail-panel"
        >
          {/* Panel header with close button */}
          <div
            className="flex items-center justify-between px-4 py-2 shrink-0"
            style={{
              backgroundColor: "var(--color-surface)",
              borderBottom: "1px solid var(--color-border)",
            }}
          >
            <span
              className="text-xs font-mono"
              style={{ color: "var(--color-text-secondary)" }}
            >
              {selectedId}
            </span>
            <button
              className="p-1 rounded cursor-pointer"
              style={{ color: "var(--color-text-tertiary)" }}
              onClick={() => setSelectedId(null)}
              data-testid="kanban-detail-close"
              aria-label="Close detail panel"
              onMouseEnter={(e) => (e.currentTarget.style.backgroundColor = "var(--color-hover)")}
              onMouseLeave={(e) => (e.currentTarget.style.backgroundColor = "transparent")}
            >
              <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
                <line x1="3" y1="3" x2="11" y2="11" />
                <line x1="11" y1="3" x2="3" y2="11" />
              </svg>
            </button>
          </div>
          <div className="flex-1 min-h-0 overflow-y-auto">
            <NodeDetailView
              nodeId={selectedId}
              nodeStore={nodeStore}
              onNavigate={handleCardSelect}
            />
          </div>
        </div>
      )}
    </div>
  );
}

/** Single Kanban column per FR-UI-21b/c. */
function KanbanColumn({
  status,
  nodes,
  focusedCardIdx,
  selectedId,
  onSelectNode,
}: {
  status: Status;
  nodes: Node[];
  focusedCardIdx: number;
  selectedId: string | null;
  onSelectNode: (nodeId: string) => void;
}) {
  return (
    <div
      className="flex flex-col min-w-[220px] w-[260px] shrink-0"
      data-testid={`kanban-column-${status}`}
    >
      {/* Column accent bar */}
      <div
        className="h-[3px] rounded-t"
        style={{ backgroundColor: STATUS_COLORS[status] }}
      />

      {/* Column header per FR-UI-21c */}
      <div
        className="px-3 py-2 text-xs font-semibold uppercase tracking-wide flex items-center gap-2"
        style={{
          color: "var(--color-text-secondary)",
          backgroundColor: "var(--color-surface)",
          borderLeft: "1px solid var(--color-border)",
          borderRight: "1px solid var(--color-border)",
        }}
      >
        <span
          className="w-2 h-2 rounded-full shrink-0"
          style={{ backgroundColor: STATUS_COLORS[status] }}
        />
        {COLUMN_LABELS[status]} ({nodes.length})
      </div>

      {/* Cards container */}
      <div
        className="flex-1 overflow-y-auto p-1.5 space-y-1.5"
        style={{
          backgroundColor: "var(--color-bg)",
          borderLeft: "1px solid var(--color-border-subtle)",
          borderRight: "1px solid var(--color-border-subtle)",
          borderBottom: "1px solid var(--color-border-subtle)",
          borderRadius: "0 0 var(--radius-md) var(--radius-md)",
        }}
      >
        {nodes.length === 0 ? (
          <div
            className="text-center py-6 text-xs"
            style={{ color: "var(--color-text-tertiary)" }}
          >
            No items
          </div>
        ) : (
          nodes.map((node, cardIdx) => (
            <KanbanCard
              key={node.id}
              node={node}
              focused={cardIdx === focusedCardIdx}
              selected={node.id === selectedId}
              onSelect={() => onSelectNode(node.id)}
            />
          ))
        )}
      </div>
    </div>
  );
}

/** Individual Kanban card per FR-UI-21d/e. */
function KanbanCard({
  node,
  focused,
  selected,
  onSelect,
}: {
  node: Node;
  focused: boolean;
  selected: boolean;
  onSelect: () => void;
}) {
  const isHighlighted = focused || selected;
  return (
    <button
      className="w-full text-left rounded cursor-pointer p-2.5 animate-fade-in"
      style={{
        backgroundColor: selected ? "var(--color-accent-muted)" : "var(--color-surface)",
        border: isHighlighted
          ? "1.5px solid var(--color-accent)"
          : "1px solid var(--color-border-subtle)",
        borderRadius: "var(--radius-md)",
        boxShadow: isHighlighted ? "0 0 0 2px var(--color-focus-ring)" : "var(--shadow-sm)",
        transition: "border-color var(--transition-fast), box-shadow var(--transition-fast), background-color var(--transition-fast)",
      }}
      data-testid={`kanban-card-${node.id}`}
      data-selected={selected ? "true" : undefined}
      onClick={onSelect}
      onMouseEnter={(e) => {
        if (!focused) {
          e.currentTarget.style.borderColor = "var(--color-border)";
          e.currentTarget.style.boxShadow = "var(--shadow-md)";
        }
      }}
      onMouseLeave={(e) => {
        if (!focused) {
          e.currentTarget.style.borderColor = "var(--color-border-subtle)";
          e.currentTarget.style.boxShadow = "var(--shadow-sm)";
        }
      }}
    >
      {/* ID and priority row */}
      <div className="flex items-center justify-between mb-1">
        <span
          className="text-[11px] font-mono"
          style={{ color: "var(--color-text-tertiary)" }}
        >
          {node.id}
        </span>
        <span
          className="w-2 h-2 rounded-full shrink-0"
          style={{ backgroundColor: PRIORITY_COLORS[node.priority] ?? PRIORITY_COLORS[3] }}
          title={`Priority ${node.priority}`}
        />
      </div>

      {/* Title */}
      <div
        className="text-sm leading-snug mb-1.5 line-clamp-2"
        style={{ color: "var(--color-text-primary)" }}
      >
        {node.title}
      </div>

      {/* Invalidated badge per FR-UI-21b */}
      {node.status === "invalidated" && (
        <span
          className="inline-block text-[10px] font-semibold uppercase px-1.5 py-0.5 rounded mb-1.5"
          style={{
            backgroundColor: "var(--color-status-invalidated-bg)",
            color: "var(--color-status-invalidated)",
          }}
        >
          invalidated
        </span>
      )}

      {/* Assignee */}
      {node.assignee && (
        <div
          className="text-[11px] truncate"
          style={{ color: "var(--color-text-secondary)" }}
        >
          {node.assignee}
        </div>
      )}

      {/* Progress bar for parent nodes per FR-UI-21d */}
      {node.child_count > 0 && (
        <div
          className="mt-2 h-1 rounded-full overflow-hidden"
          style={{ backgroundColor: "var(--color-border)" }}
          data-testid={`kanban-card-progress-${node.id}`}
        >
          <div
            className="h-full rounded-full"
            style={{
              width: `${Math.min(node.progress, 100)}%`,
              backgroundColor: "var(--color-status-done)",
              transition: "width var(--transition-base)",
            }}
          />
        </div>
      )}
    </button>
  );
}

