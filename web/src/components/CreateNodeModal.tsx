/**
 * CreateNodeModal — modal form for creating new nodes.
 * Per FR-UI-11 and requirement-ui.md § 8.3.
 * Supports title, description, prompt, priority, and optional parent.
 */

import { useState, useCallback, useMemo, useRef, useEffect } from "react";
import type { Priority } from "../types";
import * as api from "../api";
import { ALL_PROJECTS, useProjectOptional } from "../contexts/ProjectContext";
import { projectFromId, isValidPrefix } from "../utils/nodeId";

export interface CreateNodeModalProps {
  isOpen: boolean;
  onClose: () => void;
  onCreated: (node: { id: string; title: string }) => void;
  defaultParentId?: string;
}

export function CreateNodeModal({
  isOpen,
  onClose,
  onCreated,
  defaultParentId,
}: CreateNodeModalProps) {
  const projectCtx = useProjectOptional();
  const knownProjects = useMemo(() => projectCtx?.projects ?? [], [projectCtx]);
  const defaultProject = projectCtx?.defaultCreateProject ?? "";

  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [prompt, setPrompt] = useState("");
  const [priority, setPriority] = useState<Priority>(2);
  const [parentId, setParentId] = useState(defaultParentId ?? "");
  const [project, setProject] = useState(defaultProject);
  // When set, a new (unknown) project prefix is awaiting confirmation (D5).
  const [pendingNewProject, setPendingNewProject] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const titleRef = useRef<HTMLInputElement>(null);

  // A child create inherits — and is locked to — the parent's project (MP-17).
  const trimmedParent = parentId.trim();
  const isChild = trimmedParent !== "";
  const inheritedProject = isChild ? projectFromId(trimmedParent) : "";

  // Reset form when opened.
  useEffect(() => {
    if (isOpen) {
      setTitle("");
      setDescription("");
      setPrompt("");
      setPriority(2);
      setParentId(defaultParentId ?? "");
      setProject(defaultProject);
      setPendingNewProject(null);
      setError(null);
      setSubmitting(false);
      setTimeout(() => titleRef.current?.focus(), 80);
    }
    // defaultProject intentionally read at open time only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isOpen, defaultParentId]);

  const doCreate = useCallback(
    async (projectToUse: string) => {
      setSubmitting(true);
      setError(null);
      try {
        const node = await api.createNode({
          title: title.trim(),
          description: description.trim() || undefined,
          prompt: prompt.trim() || undefined,
          priority,
          parent_id: trimmedParent || undefined,
          project: projectToUse || undefined,
        } as Parameters<typeof api.createNode>[0]);
        onCreated(node);
        onClose();
      } catch (err) {
        setError(err instanceof Error ? err.message : "Failed to create node");
      } finally {
        setSubmitting(false);
      }
    },
    [title, description, prompt, priority, trimmedParent, onCreated, onClose],
  );

  const handleSubmit = useCallback(
    (e: React.FormEvent) => {
      e.preventDefault();
      if (!title.trim()) {
        setError("Title is required");
        return;
      }

      // Child: project is inherited from the parent (sent only when derivable
      // so the server can validate the match; relative parents inherit on the
      // server side).
      if (isChild) {
        void doCreate(inheritedProject);
        return;
      }

      // Root: resolve the chosen project; an unknown prefix needs confirmation.
      // The active scope and primary are existing projects by construction, so
      // they count as known even before the project list finishes loading.
      const chosen = project.trim();
      const existing = new Set<string>(knownProjects.map((p) => p.prefix));
      if (projectCtx?.primary) existing.add(projectCtx.primary);
      if (projectCtx && projectCtx.activeScope !== ALL_PROJECTS && projectCtx.activeScope) {
        existing.add(projectCtx.activeScope);
      }
      if (chosen && !existing.has(chosen)) {
        if (!isValidPrefix(chosen)) {
          setError(`Invalid project prefix "${chosen}"`);
          return;
        }
        setPendingNewProject(chosen);
        return;
      }
      void doCreate(chosen);
    },
    [title, isChild, inheritedProject, project, knownProjects, projectCtx, doCreate],
  );

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        handleSubmit(e);
      }
    },
    [onClose, handleSubmit],
  );

  if (!isOpen) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center pt-20"
      style={{ backgroundColor: "rgba(0, 0, 0, 0.5)" }}
      onClick={(e) => e.target === e.currentTarget && onClose()}
      role="dialog"
      aria-label="Create node"
      aria-modal="true"
    >
      <div
        className="w-full max-w-lg overflow-hidden animate-scale-in"
        style={{
          backgroundColor: "var(--color-surface)",
          borderRadius: "var(--radius-xl)",
          boxShadow: "var(--shadow-overlay)",
          border: "1px solid var(--color-border)",
        }}
        onKeyDown={handleKeyDown}
      >
        {/* Header */}
        <div
          className="flex items-center justify-between px-5 py-4"
          style={{ borderBottom: "1px solid var(--color-border)" }}
        >
          <h2
            className="text-sm font-semibold"
            style={{ color: "var(--color-text-primary)" }}
          >
            Create Issue
          </h2>
          <button
            onClick={onClose}
            className="p-1 rounded hover:opacity-70 cursor-pointer"
            aria-label="Close"
            style={{ color: "var(--color-text-tertiary)" }}
          >
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
              <line x1="4" y1="4" x2="12" y2="12" />
              <line x1="12" y1="4" x2="4" y2="12" />
            </svg>
          </button>
        </div>

        {/* Form */}
        <form onSubmit={handleSubmit} className="px-5 py-4">
          {/* Title */}
          <div className="mb-4">
            <input
              ref={titleRef}
              type="text"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="Issue title"
              maxLength={500}
              className="w-full text-sm py-2 px-3 rounded outline-none"
              style={{
                backgroundColor: "var(--color-bg)",
                color: "var(--color-text-primary)",
                border: "1px solid var(--color-border)",
                borderRadius: "var(--radius-md)",
              }}
              aria-label="Title"
              data-testid="create-title"
            />
          </div>

          {/* Description */}
          <div className="mb-4">
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Description (optional)"
              rows={2}
              className="w-full text-sm py-2 px-3 rounded outline-none resize-y"
              style={{
                backgroundColor: "var(--color-bg)",
                color: "var(--color-text-primary)",
                border: "1px solid var(--color-border)",
                borderRadius: "var(--radius-md)",
                minHeight: "60px",
              }}
              aria-label="Description"
              data-testid="create-description"
            />
          </div>

          {/* Prompt */}
          <div className="mb-4">
            <textarea
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              placeholder="LLM prompt (optional)"
              rows={3}
              className="w-full text-sm py-2 px-3 rounded outline-none resize-y font-mono"
              style={{
                backgroundColor: "var(--color-bg)",
                color: "var(--color-text-primary)",
                border: "1px solid var(--color-border)",
                borderRadius: "var(--radius-md)",
                minHeight: "80px",
                fontSize: "13px",
              }}
              aria-label="Prompt"
              data-testid="create-prompt"
            />
          </div>

          {/* Priority + Parent row */}
          <div className="flex gap-3 mb-4">
            <div className="flex-1">
              <label
                className="block text-xs font-medium mb-1.5"
                style={{ color: "var(--color-text-secondary)" }}
              >
                Priority
              </label>
              <div className="flex gap-1">
                {([0, 1, 2, 3, 4] as Priority[]).map((p) => (
                  <button
                    key={p}
                    type="button"
                    onClick={() => setPriority(p)}
                    className="flex-1 py-1.5 text-xs font-medium rounded cursor-pointer"
                    style={{
                      backgroundColor: priority === p ? "var(--color-accent)" : "var(--color-bg)",
                      color: priority === p ? "#ffffff" : "var(--color-text-secondary)",
                      border: `1px solid ${priority === p ? "var(--color-accent)" : "var(--color-border)"}`,
                      borderRadius: "var(--radius-sm)",
                    }}
                    data-testid={`priority-${p}`}
                  >
                    P{p}
                  </button>
                ))}
              </div>
            </div>
          </div>

          {/* Parent ID */}
          <div className="mb-4">
            <input
              type="text"
              value={parentId}
              onChange={(e) => setParentId(e.target.value)}
              placeholder="Parent ID (optional, e.g. 1.2)"
              className="w-full text-sm py-2 px-3 rounded outline-none font-mono"
              style={{
                backgroundColor: "var(--color-bg)",
                color: "var(--color-text-primary)",
                border: "1px solid var(--color-border)",
                borderRadius: "var(--radius-md)",
                fontSize: "13px",
              }}
              aria-label="Parent ID"
              data-testid="create-parent-id"
            />
          </div>

          {/* Project (MP-17) */}
          <div className="mb-4">
            <label
              className="block text-xs font-medium mb-1.5"
              style={{ color: "var(--color-text-secondary)" }}
            >
              Project
            </label>
            {isChild ? (
              // Inherited from the parent and locked — a node can never live in
              // a different project than its parent.
              <div
                className="w-full text-sm py-2 px-3 rounded font-mono flex items-center gap-2"
                style={{
                  backgroundColor: "var(--color-hover)",
                  color: "var(--color-text-secondary)",
                  border: "1px solid var(--color-border)",
                  borderRadius: "var(--radius-md)",
                  fontSize: "13px",
                }}
                data-testid="create-project-locked"
                title="Inherited from parent"
              >
                <LockIcon />
                <span>
                  {inheritedProject || "Inherited from parent"}
                </span>
              </div>
            ) : (
              <>
                <input
                  type="text"
                  list="create-project-options"
                  value={project}
                  onChange={(e) => {
                    setProject(e.target.value);
                    setPendingNewProject(null);
                  }}
                  placeholder="Project prefix (e.g. MTIX)"
                  className="w-full text-sm py-2 px-3 rounded outline-none font-mono"
                  style={{
                    backgroundColor: "var(--color-bg)",
                    color: "var(--color-text-primary)",
                    border: "1px solid var(--color-border)",
                    borderRadius: "var(--radius-md)",
                    fontSize: "13px",
                  }}
                  aria-label="Project"
                  data-testid="create-project"
                />
                <datalist id="create-project-options">
                  {knownProjects.map((p) => (
                    <option key={p.prefix} value={p.prefix} />
                  ))}
                </datalist>
              </>
            )}
          </div>

          {/* New-project confirmation step (D5) */}
          {pendingNewProject && (
            <div
              className="mb-4 px-3 py-2.5 rounded"
              style={{
                backgroundColor: "var(--color-accent-muted)",
                border: "1px solid var(--color-accent)",
                borderRadius: "var(--radius-md)",
              }}
              data-testid="new-project-confirm"
            >
              <p className="text-xs mb-2" style={{ color: "var(--color-text-primary)" }}>
                Create new project{" "}
                <span className="font-mono font-semibold">{pendingNewProject}</span>?
                This will start a new project in this database.
              </p>
              <div className="flex gap-2">
                <button
                  type="button"
                  className="px-3 py-1 text-xs rounded cursor-pointer"
                  style={{
                    color: "var(--color-text-secondary)",
                    border: "1px solid var(--color-border)",
                    borderRadius: "var(--radius-sm)",
                  }}
                  onClick={() => setPendingNewProject(null)}
                  data-testid="new-project-cancel"
                >
                  Cancel
                </button>
                <button
                  type="button"
                  className="px-3 py-1 text-xs font-medium rounded cursor-pointer"
                  style={{
                    backgroundColor: "var(--color-accent)",
                    color: "#ffffff",
                    borderRadius: "var(--radius-sm)",
                  }}
                  onClick={() => {
                    const prefix = pendingNewProject;
                    setPendingNewProject(null);
                    void doCreate(prefix);
                  }}
                  data-testid="new-project-confirm-button"
                >
                  Create project &amp; issue
                </button>
              </div>
            </div>
          )}

          {/* Error */}
          {error && (
            <div
              className="mb-4 text-xs px-3 py-2 rounded"
              style={{
                color: "var(--color-status-blocked)",
                backgroundColor: "var(--color-status-blocked-bg)",
                borderRadius: "var(--radius-md)",
              }}
              data-testid="create-error"
            >
              {error}
            </div>
          )}

          {/* Actions */}
          <div
            className="flex items-center justify-between pt-3"
            style={{ borderTop: "1px solid var(--color-border)" }}
          >
            <span className="text-xs" style={{ color: "var(--color-text-tertiary)" }}>
              <kbd>{"\u2318"}</kbd> + <kbd>Enter</kbd> to submit
            </span>
            <div className="flex gap-2">
              <button
                type="button"
                onClick={onClose}
                className="px-3 py-1.5 text-sm rounded cursor-pointer"
                style={{
                  color: "var(--color-text-secondary)",
                  border: "1px solid var(--color-border)",
                  borderRadius: "var(--radius-md)",
                }}
              >
                Cancel
              </button>
              <button
                type="submit"
                disabled={submitting || !title.trim()}
                className="px-4 py-1.5 text-sm font-medium rounded cursor-pointer"
                style={{
                  backgroundColor: submitting || !title.trim() ? "var(--color-border)" : "var(--color-accent)",
                  color: submitting || !title.trim() ? "var(--color-text-tertiary)" : "#ffffff",
                  borderRadius: "var(--radius-md)",
                  opacity: submitting ? 0.7 : 1,
                }}
                data-testid="create-submit"
              >
                {submitting ? "Creating..." : "Create Issue"}
              </button>
            </div>
          </div>
        </form>
      </div>
    </div>
  );
}

function LockIcon() {
  return (
    <svg
      width="11"
      height="11"
      viewBox="0 0 14 14"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.3"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <rect x="2.5" y="6" width="9" height="6.5" rx="1" />
      <path d="M4.5 6V4.5a2.5 2.5 0 0 1 5 0V6" />
    </svg>
  );
}
