/**
 * ContextChain — ancestry path visualization from root to current node.
 * Shows level indicator (S=Story, E=Epic, I=Issue), title, and current marker.
 * Ancestors are clickable for navigation. Collapsible with summary/expanded modes.
 * Per requirement-ui.md § 2 View A and MTIX-9.3.3.
 */

import { useState, useCallback } from "react";
import type { ContextEntry } from "../types";

/** Map depth to level indicator per node type hierarchy. */
function levelIndicator(depth: number): string {
  if (depth === 0) return "S";
  if (depth === 1) return "E";
  return "I";
}

/** Determine source attribution for a prompt. */
function sourceAttribution(prompt: string): "human" | "llm" {
  // Convention: human-authored prompts contain a [HUMAN-AUTHORED] marker.
  if (prompt.includes("[HUMAN-AUTHORED]")) return "human";
  return "llm";
}

export interface ContextChainProps {
  /** Ordered context entries from root to current node. */
  chain: ContextEntry[];
  /** ID of the current node (marked with ▶ THIS). */
  currentNodeId: string;
  /** Navigate to a node when clicked. */
  onNavigate: (nodeId: string) => void;
  /** Additional CSS class. */
  className?: string;
}

export function ContextChain({
  chain,
  currentNodeId,
  onNavigate,
  className = "",
}: ContextChainProps) {
  const [expanded, setExpanded] = useState(false);

  const toggleExpand = useCallback(() => {
    setExpanded((prev) => !prev);
  }, []);

  if (chain.length === 0) return null;

  return (
    <div
      className={`rounded border ${className}`}
      style={{
        borderColor: "var(--color-border)",
        backgroundColor: "var(--color-surface)",
      }}
      data-testid="context-chain"
    >
      {/* Header */}
      <button
        className="w-full flex items-center justify-between px-3 py-2 text-xs font-medium cursor-pointer"
        style={{ color: "var(--color-text-secondary)" }}
        onClick={toggleExpand}
        aria-expanded={expanded}
        aria-label="Toggle context chain"
      >
        <span>Context Chain</span>
        <span>{expanded ? "▾" : "▸"}</span>
      </button>

      {/* Chain entries */}
      <div className="px-3 pb-2">
        {chain.map((entry) => {
          const isCurrent = entry.id === currentNodeId;
          const level = levelIndicator(entry.depth);
          const attribution = sourceAttribution(entry.prompt);

          return (
            <div
              key={entry.id}
              className="flex items-start gap-2 py-1"
              style={{
                paddingLeft: `${entry.depth * 12}px`,
              }}
            >
              {/* Level indicator */}
              <span
                className="shrink-0 w-5 h-5 flex items-center justify-center rounded text-xs font-bold"
                style={{
                  backgroundColor: isCurrent
                    ? "var(--color-accent)"
                    : "var(--color-border)",
                  color: isCurrent
                    ? "#FFFFFF"
                    : "var(--color-text-secondary)",
                }}
                data-testid={`level-${level}`}
              >
                {level}
              </span>

              {/* Content */}
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-1.5">
                  {isCurrent && (
                    <span
                      className="text-xs font-bold shrink-0"
                      style={{ color: "var(--color-accent)" }}
                      data-testid="current-marker"
                    >
                      ▶ THIS
                    </span>
                  )}
                  <button
                    className="text-sm truncate text-left hover:underline cursor-pointer"
                    style={{
                      color: isCurrent
                        ? "var(--color-text-primary)"
                        : "var(--color-text-secondary)",
                      fontWeight: isCurrent ? 600 : 400,
                    }}
                    onClick={() => onNavigate(entry.id)}
                    data-testid={`chain-link-${entry.id}`}
                  >
                    {entry.title}
                  </button>

                  {/* Source attribution marker */}
                  <span
                    className="text-xs shrink-0 px-1 rounded"
                    style={{
                      backgroundColor:
                        attribution === "human"
                          ? "var(--color-status-in-progress)"
                          : "var(--color-status-deferred)",
                      color: "#FFFFFF",
                      opacity: 0.8,
                    }}
                    data-testid={`attribution-${attribution}`}
                  >
                    {attribution === "human"
                      ? "HUMAN"
                      : "LLM"}
                  </span>
                </div>

                {/* Expanded: show full prompt */}
                {expanded && entry.prompt && (
                  <div
                    className="mt-1 text-xs whitespace-pre-wrap rounded p-2"
                    style={{
                      color: "var(--color-text-secondary)",
                      backgroundColor: "var(--color-bg)",
                    }}
                    data-testid={`prompt-${entry.id}`}
                  >
                    {entry.prompt}
                  </div>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
