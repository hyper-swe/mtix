/**
 * PromptEditor — inline prompt editor with Human Addition blocks.
 * Per FR-UI-4, FR-UI-6, FR-UI-8, FR-UI-12 and requirement-ui.md § 3.1-3.3.
 *
 * Features:
 * - Editable markdown textarea for existing prompt.
 * - Distinct "Human Addition" block (different background, labeled).
 * - Save, Save & Rerun Children (dropdown with 4 strategies), Cancel.
 * - Prompt annotations (attributed, timestamped, resolvable).
 * - Diff view showing what changed after edit.
 */

import { useState, useCallback, useRef, useEffect } from "react";
import type { Annotation } from "../types";
import type { RerunStrategy } from "../api/nodes";

/** Compute a simple line-based diff between old and new text. */
function computeDiff(
  oldText: string,
  newText: string,
): DiffLine[] {
  const oldLines = oldText.split("\n");
  const newLines = newText.split("\n");
  const result: DiffLine[] = [];

  const maxLen = Math.max(oldLines.length, newLines.length);
  for (let i = 0; i < maxLen; i++) {
    const oldLine = oldLines[i];
    const newLine = newLines[i];

    if (oldLine === newLine) {
      if (oldLine !== undefined) {
        result.push({ type: "unchanged", text: oldLine });
      }
    } else {
      if (oldLine !== undefined) {
        result.push({ type: "removed", text: oldLine });
      }
      if (newLine !== undefined) {
        result.push({ type: "added", text: newLine });
      }
    }
  }
  return result;
}

export interface DiffLine {
  type: "added" | "removed" | "unchanged";
  text: string;
}

export interface PromptEditorProps {
  /** Current prompt text. */
  prompt: string;
  /** Existing annotations on the prompt. */
  annotations: Annotation[];
  /** Whether the editor is open. */
  isOpen: boolean;
  /** Save callback (prompt text only). */
  onSave: (prompt: string) => void;
  /** Save and rerun callback with strategy. */
  onSaveAndRerun: (prompt: string, strategy: RerunStrategy) => void;
  /** Cancel editing. */
  onCancel: () => void;
  /** Add annotation callback. */
  onAddAnnotation: (text: string) => void;
  /** Resolve annotation callback. */
  onResolveAnnotation: (annotationId: string, resolved: boolean) => void;
  /** Additional CSS class. */
  className?: string;
}

export function PromptEditor({
  prompt,
  annotations,
  isOpen,
  onSave,
  onSaveAndRerun,
  onCancel,
  onAddAnnotation,
  onResolveAnnotation,
  className = "",
}: PromptEditorProps) {
  const [editedPrompt, setEditedPrompt] = useState(prompt);
  const [humanAddition, setHumanAddition] = useState("");
  const [showRerunMenu, setShowRerunMenu] = useState(false);
  const [showDiff, setShowDiff] = useState(false);
  const [annotationText, setAnnotationText] = useState("");
  const [showAnnotationInput, setShowAnnotationInput] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const rerunMenuRef = useRef<HTMLDivElement>(null);

  // Reset state when editor opens.
  useEffect(() => {
    if (isOpen) {
      setEditedPrompt(prompt);
      setHumanAddition("");
      setShowRerunMenu(false);
      setShowDiff(false);
      setAnnotationText("");
      setShowAnnotationInput(false);
    }
  }, [isOpen, prompt]);

  // Focus textarea when editor opens.
  useEffect(() => {
    if (isOpen && textareaRef.current) {
      textareaRef.current.focus();
    }
  }, [isOpen]);

  // Close rerun menu on outside click.
  useEffect(() => {
    if (!showRerunMenu) return;
    const handler = (e: MouseEvent) => {
      if (
        rerunMenuRef.current &&
        !rerunMenuRef.current.contains(e.target as globalThis.Node)
      ) {
        setShowRerunMenu(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showRerunMenu]);

  const fullPrompt = useCallback(() => {
    const parts = [editedPrompt.trim()];
    if (humanAddition.trim()) {
      parts.push(`\n\n[HUMAN-AUTHORED]\n${humanAddition.trim()}`);
    }
    return parts.join("");
  }, [editedPrompt, humanAddition]);

  const handleSave = useCallback(() => {
    onSave(fullPrompt());
  }, [onSave, fullPrompt]);

  const handleRerun = useCallback(
    (strategy: RerunStrategy) => {
      setShowRerunMenu(false);
      onSaveAndRerun(fullPrompt(), strategy);
    },
    [onSaveAndRerun, fullPrompt],
  );

  const handleAddAnnotation = useCallback(() => {
    if (annotationText.trim()) {
      onAddAnnotation(annotationText.trim());
      setAnnotationText("");
      setShowAnnotationInput(false);
    }
  }, [annotationText, onAddAnnotation]);

  const diff = computeDiff(prompt, fullPrompt());
  const hasChanges = editedPrompt.trim() !== prompt.trim() || humanAddition.trim() !== "";

  if (!isOpen) return null;

  const rerunStrategies: { strategy: RerunStrategy; label: string }[] = [
    { strategy: "all", label: "Rerun all children" },
    { strategy: "open_only", label: "Rerun open only" },
    { strategy: "delete", label: "Delete & re-decompose" },
    { strategy: "review", label: "Manual review" },
  ];

  return (
    <div
      className={`rounded border ${className}`}
      style={{
        borderColor: "var(--color-border)",
        backgroundColor: "var(--color-surface)",
      }}
      data-testid="prompt-editor"
    >
      {/* Header */}
      <div
        className="px-3 py-2 text-xs font-medium border-b"
        style={{
          color: "var(--color-text-secondary)",
          borderColor: "var(--color-border)",
        }}
      >
        Prompt Editor
      </div>

      {/* Main prompt textarea */}
      <div className="p-3">
        <textarea
          ref={textareaRef}
          className="w-full rounded border p-2 text-sm font-mono resize-y"
          style={{
            backgroundColor: "var(--color-bg)",
            borderColor: "var(--color-border)",
            color: "var(--color-text-primary)",
            minHeight: "120px",
          }}
          value={editedPrompt}
          onChange={(e) => setEditedPrompt(e.target.value)}
          aria-label="Prompt text"
          data-testid="prompt-textarea"
        />

        {/* Human Addition block */}
        <div
          className="mt-3 rounded border p-2"
          style={{
            backgroundColor: "var(--color-status-in-progress)",
            opacity: 0.1,
          }}
        />
        <div
          className="mt-2 rounded border p-2"
          style={{
            borderColor: "var(--color-status-in-progress)",
            backgroundColor: "rgba(59, 130, 246, 0.05)",
          }}
          data-testid="human-addition-block"
        >
          <label
            className="block text-xs font-medium mb-1"
            style={{ color: "var(--color-status-in-progress)" }}
          >
            Human Addition
          </label>
          <textarea
            className="w-full rounded border p-2 text-sm font-mono resize-y"
            style={{
              backgroundColor: "var(--color-bg)",
              borderColor: "var(--color-status-in-progress)",
              color: "var(--color-text-primary)",
              minHeight: "60px",
            }}
            value={humanAddition}
            onChange={(e) => setHumanAddition(e.target.value)}
            placeholder="Add human instructions here…"
            aria-label="Human addition"
            data-testid="human-addition-textarea"
          />
        </div>

        {/* Diff view toggle */}
        {hasChanges && (
          <div className="mt-2">
            <button
              className="text-xs cursor-pointer underline"
              style={{ color: "var(--color-accent)" }}
              onClick={() => setShowDiff(!showDiff)}
              data-testid="diff-toggle"
            >
              {showDiff ? "Hide diff" : "Show diff"}
            </button>

            {showDiff && (
              <div
                className="mt-2 rounded border p-2 text-xs font-mono overflow-x-auto"
                style={{
                  borderColor: "var(--color-border)",
                  backgroundColor: "var(--color-bg)",
                }}
                data-testid="diff-view"
              >
                {diff.map((line, i) => (
                  <div
                    key={i}
                    style={{
                      color:
                        line.type === "added"
                          ? "var(--color-status-done)"
                          : line.type === "removed"
                            ? "var(--color-status-blocked)"
                            : "var(--color-text-secondary)",
                      backgroundColor:
                        line.type === "added"
                          ? "rgba(16, 185, 129, 0.1)"
                          : line.type === "removed"
                            ? "rgba(239, 68, 68, 0.1)"
                            : "transparent",
                    }}
                    data-testid={`diff-line-${line.type}`}
                  >
                    {line.type === "added"
                      ? "+ "
                      : line.type === "removed"
                        ? "- "
                        : "  "}
                    {line.text}
                  </div>
                ))}
              </div>
            )}
          </div>
        )}

        {/* Action buttons */}
        <div className="mt-3 flex items-center gap-2">
          <button
            className="px-3 py-1.5 text-xs font-medium rounded cursor-pointer"
            style={{
              backgroundColor: "var(--color-accent)",
              color: "#FFFFFF",
            }}
            onClick={handleSave}
            data-testid="save-button"
          >
            Save
          </button>

          {/* Save & Rerun dropdown */}
          <div className="relative" ref={rerunMenuRef}>
            <button
              className="px-3 py-1.5 text-xs font-medium rounded cursor-pointer"
              style={{
                backgroundColor: "var(--color-status-in-progress)",
                color: "#FFFFFF",
              }}
              onClick={() => setShowRerunMenu(!showRerunMenu)}
              data-testid="save-rerun-button"
            >
              Save & Rerun Children ▾
            </button>

            {showRerunMenu && (
              <div
                className="absolute left-0 top-full mt-1 w-52 rounded border shadow-lg z-10"
                style={{
                  backgroundColor: "var(--color-surface)",
                  borderColor: "var(--color-border)",
                }}
                data-testid="rerun-menu"
              >
                {rerunStrategies.map((s) => (
                  <button
                    key={s.strategy}
                    className="w-full text-left px-3 py-2 text-xs hover:opacity-80 cursor-pointer"
                    style={{ color: "var(--color-text-primary)" }}
                    onClick={() => handleRerun(s.strategy)}
                    data-testid={`rerun-${s.strategy}`}
                  >
                    {s.label}
                  </button>
                ))}
              </div>
            )}
          </div>

          <button
            className="px-3 py-1.5 text-xs font-medium rounded cursor-pointer"
            style={{
              color: "var(--color-text-secondary)",
              border: "1px solid var(--color-border)",
            }}
            onClick={onCancel}
            data-testid="cancel-button"
          >
            Cancel
          </button>
        </div>
      </div>

      {/* Annotations section */}
      <div
        className="border-t px-3 py-2"
        style={{ borderColor: "var(--color-border)" }}
      >
        <div className="flex items-center justify-between mb-2">
          <span
            className="text-xs font-medium"
            style={{ color: "var(--color-text-secondary)" }}
          >
            Annotations ({annotations.length})
          </span>
          <button
            className="text-xs cursor-pointer"
            style={{ color: "var(--color-accent)" }}
            onClick={() => setShowAnnotationInput(!showAnnotationInput)}
            data-testid="add-annotation-button"
          >
            + Add
          </button>
        </div>

        {/* Annotation input */}
        {showAnnotationInput && (
          <div className="flex gap-2 mb-2" data-testid="annotation-input">
            <input
              className="flex-1 rounded border px-2 py-1 text-xs"
              style={{
                backgroundColor: "var(--color-bg)",
                borderColor: "var(--color-border)",
                color: "var(--color-text-primary)",
              }}
              value={annotationText}
              onChange={(e) => setAnnotationText(e.target.value)}
              placeholder="Add annotation…"
              aria-label="Annotation text"
              onKeyDown={(e) => {
                if (e.key === "Enter") handleAddAnnotation();
              }}
            />
            <button
              className="px-2 py-1 text-xs rounded cursor-pointer"
              style={{
                backgroundColor: "var(--color-accent)",
                color: "#FFFFFF",
              }}
              onClick={handleAddAnnotation}
              data-testid="submit-annotation"
            >
              Add
            </button>
          </div>
        )}

        {/* Existing annotations */}
        {annotations.map((ann) => (
          <div
            key={ann.id}
            className="flex items-start gap-2 py-1.5 border-b last:border-b-0"
            style={{
              borderColor: "var(--color-border)",
              opacity: ann.resolved ? 0.5 : 1,
            }}
            data-testid="annotation-item"
          >
            <span className="text-xs shrink-0">💬</span>
            <div className="flex-1 min-w-0">
              <div className="flex items-baseline gap-1.5">
                <span
                  className="text-xs font-medium"
                  style={{ color: "var(--color-text-primary)" }}
                >
                  {ann.author}
                </span>
                <span
                  className="text-xs"
                  style={{ color: "var(--color-text-secondary)" }}
                >
                  {new Date(ann.created_at).toLocaleString()}
                </span>
              </div>
              <p
                className="text-xs mt-0.5"
                style={{
                  color: "var(--color-text-secondary)",
                  textDecoration: ann.resolved ? "line-through" : "none",
                }}
              >
                {ann.text}
              </p>
            </div>
            <button
              className="text-xs shrink-0 cursor-pointer"
              style={{ color: "var(--color-accent)" }}
              onClick={() => onResolveAnnotation(ann.id, !ann.resolved)}
              data-testid={`resolve-annotation-${ann.id}`}
            >
              {ann.resolved ? "Reopen" : "Resolve"}
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}
