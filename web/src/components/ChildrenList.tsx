/**
 * ChildrenList — shows child nodes with quick-add bar.
 * Per FR-UI-11 and requirement-ui.md § 8.3.
 * Supports multi-select with Shift per FR-UI-14 for bulk actions.
 */

import { useState, useCallback, useRef } from "react";
import type { Node } from "../types";
import { StatusIcon } from "./StatusIcon";

export interface ChildrenListProps {
  /** Child nodes. */
  children: Node[];
  /** Parent node ID for creating children. */
  parentId: string;
  /** Callback when a child is selected. */
  onSelect: (nodeId: string) => void;
  /** Callback to create a new child. */
  onCreate: (title: string, prompt?: string) => void;
  /** Callback for bulk status change on selected nodes. */
  onBulkAction?: (nodeIds: string[], action: string) => void;
  /** Additional CSS class. */
  className?: string;
}

export function ChildrenList({
  children,
  parentId: _parentId,
  onSelect,
  onCreate,
  onBulkAction,
  className = "",
}: ChildrenListProps) {
  const [newTitle, setNewTitle] = useState("");
  const [showPromptField, setShowPromptField] = useState(false);
  const [newPrompt, setNewPrompt] = useState("");
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const lastClickedRef = useRef<number | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const handleCreate = useCallback(() => {
    if (newTitle.trim()) {
      onCreate(
        newTitle.trim(),
        showPromptField && newPrompt.trim() ? newPrompt.trim() : undefined,
      );
      setNewTitle("");
      setNewPrompt("");
      setShowPromptField(false);
      inputRef.current?.focus();
    }
  }, [newTitle, newPrompt, showPromptField, onCreate]);

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === "Enter") {
        handleCreate();
      } else if (e.key === "Tab" && !showPromptField) {
        e.preventDefault();
        setShowPromptField(true);
      }
    },
    [handleCreate, showPromptField],
  );

  const handleChildClick = useCallback(
    (nodeId: string, index: number, e: React.MouseEvent) => {
      if (e.shiftKey && lastClickedRef.current !== null) {
        // Multi-select with Shift.
        const start = Math.min(lastClickedRef.current, index);
        const end = Math.max(lastClickedRef.current, index);
        const ids = new Set(selectedIds);
        for (let i = start; i <= end; i++) {
          const child = children[i];
          if (child) ids.add(child.id);
        }
        setSelectedIds(ids);
      } else if (e.metaKey || e.ctrlKey) {
        // Toggle single selection.
        const ids = new Set(selectedIds);
        if (ids.has(nodeId)) {
          ids.delete(nodeId);
        } else {
          ids.add(nodeId);
        }
        setSelectedIds(ids);
      } else {
        setSelectedIds(new Set());
        onSelect(nodeId);
      }
      lastClickedRef.current = index;
    },
    [children, selectedIds, onSelect],
  );

  const handleBulkAction = useCallback(
    (action: string) => {
      if (onBulkAction && selectedIds.size > 0) {
        onBulkAction(Array.from(selectedIds), action);
        setSelectedIds(new Set());
      }
    },
    [selectedIds, onBulkAction],
  );

  return (
    <div
      className={`rounded border ${className}`}
      style={{
        borderColor: "var(--color-border)",
        backgroundColor: "var(--color-surface)",
      }}
      data-testid="children-list"
    >
      {/* Header */}
      <div
        className="px-3 py-2 text-xs font-medium border-b flex items-center justify-between"
        style={{
          color: "var(--color-text-secondary)",
          borderColor: "var(--color-border)",
        }}
      >
        <span>Children ({children.length})</span>
      </div>

      {/* Bulk action bar */}
      {selectedIds.size > 0 && (
        <div
          className="px-3 py-1.5 flex items-center gap-2 border-b"
          style={{
            borderColor: "var(--color-border)",
            backgroundColor: "var(--color-accent)",
          }}
          data-testid="bulk-actions"
        >
          <span className="text-xs text-white">
            {selectedIds.size} selected
          </span>
          <button
            className="text-xs text-white underline cursor-pointer"
            onClick={() => handleBulkAction("done")}
            data-testid="bulk-done"
          >
            Mark Done
          </button>
          <button
            className="text-xs text-white underline cursor-pointer"
            onClick={() => handleBulkAction("cancel")}
            data-testid="bulk-cancel"
          >
            Cancel
          </button>
        </div>
      )}

      {/* Children */}
      {children.length === 0 ? (
        <p
          className="px-3 py-4 text-xs text-center"
          style={{ color: "var(--color-text-secondary)" }}
        >
          No children
        </p>
      ) : (
        <div>
          {children.map((child, index) => (
            <button
              key={child.id}
              className="w-full flex items-center gap-2 px-3 py-1.5 text-left cursor-pointer border-b last:border-b-0"
              style={{
                borderColor: "var(--color-border)",
                backgroundColor: selectedIds.has(child.id)
                  ? "rgba(99, 102, 241, 0.1)"
                  : "transparent",
              }}
              onClick={(e) => handleChildClick(child.id, index, e)}
              data-testid={`child-${child.id}`}
            >
              <StatusIcon status={child.status} size={14} />
              <span
                className="text-xs font-mono"
                style={{ color: "var(--color-text-secondary)" }}
              >
                .{child.seq}
              </span>
              <span
                className="text-sm truncate"
                style={{
                  color: "var(--color-text-primary)",
                  textDecoration:
                    child.status === "done" || child.status === "cancelled"
                      ? "line-through"
                      : "none",
                }}
              >
                {child.title}
              </span>
            </button>
          ))}
        </div>
      )}

      {/* Quick-add bar per FR-UI-11 */}
      <div
        className="border-t px-3 py-2"
        style={{ borderColor: "var(--color-border)" }}
        data-testid="quick-add-bar"
      >
        <div className="flex items-center gap-2">
          <span
            className="text-xs shrink-0"
            style={{ color: "var(--color-text-secondary)" }}
          >
            +
          </span>
          <input
            ref={inputRef}
            className="flex-1 text-sm border-none outline-none bg-transparent"
            style={{ color: "var(--color-text-primary)" }}
            value={newTitle}
            onChange={(e) => setNewTitle(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Add micro issue…"
            aria-label="New child title"
            data-testid="quick-add-input"
          />
        </div>
        {showPromptField && (
          <textarea
            className="w-full mt-1 rounded border p-1.5 text-xs font-mono resize-y"
            style={{
              backgroundColor: "var(--color-bg)",
              borderColor: "var(--color-border)",
              color: "var(--color-text-primary)",
              minHeight: "40px",
            }}
            value={newPrompt}
            onChange={(e) => setNewPrompt(e.target.value)}
            placeholder="Prompt (optional)…"
            aria-label="New child prompt"
            data-testid="quick-add-prompt"
          />
        )}
        {newTitle.trim() && (
          <div className="flex gap-2 mt-1">
            <span
              className="text-xs"
              style={{ color: "var(--color-text-secondary)" }}
            >
              Enter to create{!showPromptField ? ", Tab for prompt" : ""}
            </span>
          </div>
        )}
      </div>
    </div>
  );
}
