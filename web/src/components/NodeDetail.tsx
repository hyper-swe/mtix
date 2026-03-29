/**
 * NodeDetail — main content panel for viewing and editing a node.
 * Integrates all sub-components: header, progress, prompt, children,
 * context chain, and tabbed sections (Description, Activity, Deps).
 * Per FR-9.4 and MTIX-9.3.1.
 */

import { useState, useCallback, useRef, useEffect } from "react";
import type {
  Node,
  Status,
  ContextEntry,
  ActivityEntry,
  Annotation,
  Dependency,
} from "../types";
import type { RerunStrategy } from "../api/nodes";
import { StatusBadge } from "./StatusBadge";
import { StatusIcon } from "./StatusIcon";
import { ProgressBar } from "./ProgressBar";
import { PromptEditor } from "./PromptEditor";
import { ChildrenList } from "./ChildrenList";
import { ContextChain } from "./ContextChain";
import { ActivityStream } from "./ActivityStream";
import { PriorityLabel } from "../types/node";

/** Available tabs in the detail panel. */
type DetailTab = "description" | "activity" | "deps";

export interface NodeDetailProps {
  /** The node to display. */
  node: Node;
  /** Child nodes. */
  children: Node[];
  /** Context chain entries (root to current). */
  contextChain: ContextEntry[];
  /** Activity entries for the Activity tab. */
  activityEntries: ActivityEntry[];
  /** Whether more activity entries are available. */
  activityHasMore: boolean;
  /** Load more activity entries. */
  onLoadMoreActivity: () => void;
  /** Whether activity is loading. */
  activityLoading?: boolean;
  /** Dependencies for the Deps tab. */
  dependencies: Dependency[];
  /** Update node title. */
  onUpdateTitle: (title: string) => void;
  /** Update node status. */
  onStatusChange: (status: Status) => void;
  /** Save prompt. */
  onSavePrompt: (prompt: string) => void;
  /** Save prompt and rerun children. */
  onSaveAndRerun: (prompt: string, strategy: RerunStrategy) => void;
  /** Add annotation. */
  onAddAnnotation: (text: string) => void;
  /** Resolve annotation. */
  onResolveAnnotation: (annotationId: string, resolved: boolean) => void;
  /** Navigate to a different node. */
  onNavigate: (nodeId: string) => void;
  /** Create a child node. */
  onCreateChild: (title: string, prompt?: string) => void;
  /** Bulk action on children. */
  onBulkAction?: (nodeIds: string[], action: string) => void;
  /** Additional CSS class. */
  className?: string;
}

export function NodeDetail({
  node,
  children: childNodes,
  contextChain,
  activityEntries,
  activityHasMore,
  onLoadMoreActivity,
  activityLoading = false,
  dependencies,
  onUpdateTitle,
  onStatusChange,
  onSavePrompt,
  onSaveAndRerun,
  onAddAnnotation,
  onResolveAnnotation,
  onNavigate,
  onCreateChild,
  onBulkAction,
  className = "",
}: NodeDetailProps) {
  const [activeTab, setActiveTab] = useState<DetailTab>("description");
  const [isEditingTitle, setIsEditingTitle] = useState(false);
  const [editedTitle, setEditedTitle] = useState(node.title);
  const [isPromptEditorOpen, setIsPromptEditorOpen] = useState(false);
  const titleInputRef = useRef<HTMLInputElement>(null);

  // Reset state when node changes.
  useEffect(() => {
    setEditedTitle(node.title);
    setIsEditingTitle(false);
    setIsPromptEditorOpen(false);
  }, [node.id, node.title]);

  // Focus title input when editing starts.
  useEffect(() => {
    if (isEditingTitle && titleInputRef.current) {
      titleInputRef.current.focus();
      titleInputRef.current.select();
    }
  }, [isEditingTitle]);

  // Handle keyboard shortcuts.
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      // Skip if focused in an input.
      const tag = (e.target as HTMLElement).tagName;
      if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;

      if (e.key === "e") {
        e.preventDefault();
        setIsEditingTitle(true);
      } else if (e.key === "p") {
        e.preventDefault();
        setIsPromptEditorOpen(true);
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, []);

  const handleTitleSave = useCallback(() => {
    if (editedTitle.trim() && editedTitle.trim() !== node.title) {
      onUpdateTitle(editedTitle.trim());
    }
    setIsEditingTitle(false);
  }, [editedTitle, node.title, onUpdateTitle]);

  const handleTitleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === "Enter") {
        handleTitleSave();
      } else if (e.key === "Escape") {
        setEditedTitle(node.title);
        setIsEditingTitle(false);
      }
    },
    [handleTitleSave, node.title],
  );

  const tabs: { key: DetailTab; label: string; count?: number }[] = [
    { key: "description", label: "Description" },
    { key: "activity", label: "Activity", count: activityEntries.length },
    { key: "deps", label: "Deps", count: dependencies.length },
  ];

  const annotations: Annotation[] = node.annotations ?? [];

  return (
    <div
      className={`flex flex-col h-full overflow-y-auto ${className}`}
      data-testid="node-detail"
    >
      {/* Header section */}
      <div
        className="p-4 border-b"
        style={{ borderColor: "var(--color-border)" }}
      >
        {/* Node ID */}
        <span
          className="text-xs font-mono"
          style={{ color: "var(--color-text-secondary)" }}
          data-testid="node-id"
        >
          {node.id}
        </span>

        {/* Title — inline editable */}
        <div className="mt-1">
          {isEditingTitle ? (
            <input
              ref={titleInputRef}
              className="w-full text-lg font-medium border-b-2 outline-none bg-transparent"
              style={{
                color: "var(--color-text-primary)",
                borderColor: "var(--color-accent)",
              }}
              value={editedTitle}
              onChange={(e) => setEditedTitle(e.target.value)}
              onBlur={handleTitleSave}
              onKeyDown={handleTitleKeyDown}
              aria-label="Edit title"
              data-testid="title-input"
            />
          ) : (
            <h1
              className="text-lg font-medium cursor-pointer"
              style={{ color: "var(--color-text-primary)" }}
              onClick={() => setIsEditingTitle(true)}
              data-testid="node-title"
            >
              {node.title}
            </h1>
          )}
        </div>

        {/* Status, priority, assignee row */}
        <div className="flex items-center gap-3 mt-2">
          <StatusBadge
            status={node.status}
            onStatusChange={onStatusChange}
          />

          <span
            className="text-xs font-medium px-1.5 py-0.5 rounded"
            style={{
              backgroundColor: "var(--color-border)",
              color: "var(--color-text-primary)",
            }}
            data-testid="priority-badge"
          >
            P{node.priority} {PriorityLabel[node.priority]}
          </span>

          {node.assignee && (
            <span
              className="text-xs flex items-center gap-1"
              style={{ color: "var(--color-text-secondary)" }}
              data-testid="assignee"
            >
              <StatusIcon status={node.status} size={12} />
              {node.assignee}
            </span>
          )}
        </div>

        {/* Progress bar */}
        <div className="mt-3" data-testid="detail-progress">
          <ProgressBar progress={node.progress} showLabel />
        </div>
      </div>

      {/* Prompt section */}
      <div
        className="p-4 border-b"
        style={{ borderColor: "var(--color-border)" }}
      >
        {isPromptEditorOpen ? (
          <PromptEditor
            prompt={node.prompt}
            annotations={annotations}
            isOpen={isPromptEditorOpen}
            onSave={(prompt) => {
              onSavePrompt(prompt);
              setIsPromptEditorOpen(false);
            }}
            onSaveAndRerun={(prompt, strategy) => {
              onSaveAndRerun(prompt, strategy);
              setIsPromptEditorOpen(false);
            }}
            onCancel={() => setIsPromptEditorOpen(false)}
            onAddAnnotation={onAddAnnotation}
            onResolveAnnotation={onResolveAnnotation}
          />
        ) : (
          <div data-testid="prompt-section">
            <div className="flex items-center justify-between mb-1">
              <span
                className="text-xs font-medium"
                style={{ color: "var(--color-text-secondary)" }}
              >
                Prompt
              </span>
              <button
                className="text-xs cursor-pointer"
                style={{ color: "var(--color-accent)" }}
                onClick={() => setIsPromptEditorOpen(true)}
                data-testid="edit-prompt-button"
              >
                Edit
              </button>
            </div>
            <div
              className="text-sm whitespace-pre-wrap rounded p-2"
              style={{
                color: "var(--color-text-primary)",
                backgroundColor: "var(--color-bg)",
              }}
              data-testid="prompt-display"
            >
              {node.prompt || "No prompt"}
            </div>
          </div>
        )}
      </div>

      {/* Children list */}
      <div
        className="p-4 border-b"
        style={{ borderColor: "var(--color-border)" }}
      >
        <ChildrenList
          children={childNodes}
          parentId={node.id}
          onSelect={onNavigate}
          onCreate={onCreateChild}
          onBulkAction={onBulkAction}
        />
      </div>

      {/* Context chain */}
      <div
        className="p-4 border-b"
        style={{ borderColor: "var(--color-border)" }}
      >
        <ContextChain
          chain={contextChain}
          currentNodeId={node.id}
          onNavigate={onNavigate}
        />
      </div>

      {/* Tabbed section: Description, Activity, Deps */}
      <div className="p-4 flex-1">
        {/* Tab bar */}
        <div
          className="flex gap-0 border-b mb-3"
          style={{ borderColor: "var(--color-border)" }}
          data-testid="tab-bar"
        >
          {tabs.map((tab) => (
            <button
              key={tab.key}
              className="px-3 py-1.5 text-sm cursor-pointer flex items-center gap-1.5"
              style={{
                color:
                  activeTab === tab.key
                    ? "var(--color-accent)"
                    : "var(--color-text-secondary)",
                borderBottom:
                  activeTab === tab.key
                    ? "2px solid var(--color-accent)"
                    : "2px solid transparent",
                fontWeight: activeTab === tab.key ? 600 : 400,
              }}
              onClick={() => setActiveTab(tab.key)}
              data-testid={`tab-${tab.key}`}
            >
              {tab.label}
              {tab.count !== undefined && tab.count > 0 && (
                <span
                  className="text-[10px] font-medium px-1.5 py-0.5 rounded-full leading-none"
                  style={{
                    backgroundColor: activeTab === tab.key
                      ? "var(--color-accent)"
                      : "var(--color-border)",
                    color: activeTab === tab.key
                      ? "var(--color-bg)"
                      : "var(--color-text-secondary)",
                  }}
                  data-testid={`tab-${tab.key}-count`}
                >
                  {tab.count}
                </span>
              )}
            </button>
          ))}
        </div>

        {/* Tab content */}
        {activeTab === "description" && (
          <div
            className="text-sm whitespace-pre-wrap"
            style={{ color: "var(--color-text-primary)" }}
            data-testid="description-content"
          >
            {node.description || "No description"}
          </div>
        )}

        {activeTab === "activity" && (
          <ActivityStream
            entries={activityEntries}
            hasMore={activityHasMore}
            onLoadMore={onLoadMoreActivity}
            loading={activityLoading}
          />
        )}

        {activeTab === "deps" && (
          <div data-testid="deps-content">
            {dependencies.length === 0 ? (
              <p
                className="text-xs text-center py-4"
                style={{ color: "var(--color-text-secondary)" }}
              >
                No dependencies
              </p>
            ) : (
              <div className="space-y-1">
                {dependencies.map((dep) => (
                  <div
                    key={`${dep.from_id}-${dep.to_id}`}
                    className="flex items-center gap-2 text-xs py-1"
                    data-testid="dep-item"
                  >
                    <span
                      className="px-1.5 py-0.5 rounded font-mono"
                      style={{
                        backgroundColor: "var(--color-border)",
                        color: "var(--color-text-primary)",
                      }}
                    >
                      {dep.dep_type}
                    </span>
                    <button
                      className="text-xs cursor-pointer hover:underline"
                      style={{ color: "var(--color-accent)" }}
                      onClick={() => onNavigate(dep.to_id)}
                    >
                      {dep.to_id}
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
