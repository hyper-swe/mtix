import { useCallback, useEffect, useRef, useState } from "react";
import type { Node } from "../types";
import * as api from "../api";
import { StatusIcon } from "./StatusIcon";

interface CommandPaletteProps {
  isOpen: boolean;
  onClose: () => void;
  onSelectNode: (nodeId: string) => void;
  onAction?: (action: string) => void;
}

/** Debounce delay for search in milliseconds. */
const DEBOUNCE_MS = 150;

/** Maximum recent items to display. */
const MAX_RECENT = 5;

/** Storage key for recent items. */
const RECENT_KEY = "mtix-recent-nodes";

/** Built-in palette actions. */
const PALETTE_ACTIONS = [
  { id: "create-node", label: "Create node", shortcut: "c" },
  { id: "switch-view", label: "Switch view", shortcut: "" },
  { id: "settings", label: "Settings", shortcut: "" },
];

/**
 * Command palette per FR-UI-3 and requirement-ui.md section 8.1.
 * Fuzzy search, recent items, actions, filter shortcuts.
 */
export function CommandPalette({
  isOpen,
  onClose,
  onSelectNode,
  onAction,
}: CommandPaletteProps) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<Node[]>([]);
  const [selectedIndex, setSelectedIndex] = useState(0);
  const [loading, setLoading] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Load recent items from storage.
  const [recentIds] = useState<string[]>(() => {
    try {
      const stored = window.localStorage.getItem(RECENT_KEY);
      return stored ? (JSON.parse(stored) as string[]).slice(0, MAX_RECENT) : [];
    } catch {
      return [];
    }
  });

  // Focus input on open.
  useEffect(() => {
    if (isOpen) {
      setQuery("");
      setResults([]);
      setSelectedIndex(0);
      setTimeout(() => inputRef.current?.focus(), 50);
    }
  }, [isOpen]);

  // Debounced search.
  useEffect(() => {
    if (!query.trim()) {
      setResults([]);
      setSelectedIndex(0);
      return;
    }

    if (debounceRef.current) {
      clearTimeout(debounceRef.current);
    }

    debounceRef.current = setTimeout(async () => {
      setLoading(true);
      try {
        const nodes = await api.searchNodes(query, { limit: 10 });
        setResults(nodes);
        setSelectedIndex(0);
      } catch {
        setResults([]);
      } finally {
        setLoading(false);
      }
    }, DEBOUNCE_MS);

    return () => {
      if (debounceRef.current) {
        clearTimeout(debounceRef.current);
      }
    };
  }, [query]);

  // Keyboard navigation within palette.
  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      const totalItems = query.trim()
        ? results.length
        : recentIds.length + PALETTE_ACTIONS.length;

      if (e.key === "ArrowDown") {
        e.preventDefault();
        setSelectedIndex((prev) => Math.min(prev + 1, totalItems - 1));
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        setSelectedIndex((prev) => Math.max(prev - 1, 0));
      } else if (e.key === "Enter") {
        e.preventDefault();
        selectCurrent();
      } else if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
    },
    [query, results, recentIds, selectedIndex],
  );

  const selectCurrent = useCallback(() => {
    if (query.trim() && results[selectedIndex]) {
      const node = results[selectedIndex];
      if (node) {
        addToRecent(node.id);
        onSelectNode(node.id);
        onClose();
      }
    } else if (!query.trim()) {
      if (selectedIndex < recentIds.length) {
        const id = recentIds[selectedIndex];
        if (id) {
          onSelectNode(id);
          onClose();
        }
      } else {
        const actionIdx = selectedIndex - recentIds.length;
        const action = PALETTE_ACTIONS[actionIdx];
        if (action) {
          onAction?.(action.id);
          onClose();
        }
      }
    }
  }, [query, results, recentIds, selectedIndex, onSelectNode, onAction, onClose]);

  // Close on backdrop click.
  const handleBackdropClick = useCallback(
    (e: React.MouseEvent) => {
      if (e.target === e.currentTarget) {
        onClose();
      }
    },
    [onClose],
  );

  if (!isOpen) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center pt-20"
      style={{ backgroundColor: "rgba(0, 0, 0, 0.5)" }}
      onClick={handleBackdropClick}
      role="dialog"
      aria-label="Command palette"
    >
      <div
        className="w-full max-w-lg overflow-hidden animate-scale-in"
        style={{
          backgroundColor: "var(--color-surface)",
          borderRadius: "var(--radius-xl)",
          boxShadow: "var(--shadow-overlay)",
          border: "1px solid var(--color-border)",
        }}
      >
        {/* Search input */}
        <div
          className="flex items-center px-4 py-3"
          style={{ borderBottom: "1px solid var(--color-border)" }}
        >
          <SearchIcon />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Search nodes, run commands..."
            className="flex-1 ml-2.5 bg-transparent outline-none text-sm"
            style={{ color: "var(--color-text-primary)" }}
            aria-label="Search"
          />
          {loading && (
            <span
              className="text-xs"
              style={{ color: "var(--color-text-tertiary)" }}
            >
              ...
            </span>
          )}
          <kbd className="ml-2">esc</kbd>
        </div>

        {/* Results */}
        <div className="max-h-80 overflow-y-auto py-1">
          {query.trim() ? (
            // Search results.
            results.length > 0 ? (
              results.map((node, idx) => (
                <PaletteItem
                  key={node.id}
                  isSelected={idx === selectedIndex}
                  onClick={() => {
                    addToRecent(node.id);
                    onSelectNode(node.id);
                    onClose();
                  }}
                >
                  <StatusIcon status={node.status} size={12} />
                  <span
                    className="text-xs font-mono mr-2 flex-shrink-0"
                    style={{ color: "var(--color-text-tertiary)" }}
                  >
                    {node.id}
                  </span>
                  <span className="text-sm truncate">{node.title}</span>
                </PaletteItem>
              ))
            ) : (
              <div
                className="px-4 py-4 text-sm text-center"
                style={{ color: "var(--color-text-tertiary)" }}
              >
                No results found
              </div>
            )
          ) : (
            // Default view: recent items + actions.
            <>
              {recentIds.length > 0 && (
                <>
                  <SectionHeader>Recent</SectionHeader>
                  {recentIds.map((id, idx) => (
                    <PaletteItem
                      key={id}
                      isSelected={idx === selectedIndex}
                      onClick={() => {
                        onSelectNode(id);
                        onClose();
                      }}
                    >
                      <span className="text-sm font-mono">{id}</span>
                    </PaletteItem>
                  ))}
                </>
              )}
              <SectionHeader>Actions</SectionHeader>
              {PALETTE_ACTIONS.map((action, idx) => (
                <PaletteItem
                  key={action.id}
                  isSelected={recentIds.length + idx === selectedIndex}
                  onClick={() => {
                    onAction?.(action.id);
                    onClose();
                  }}
                >
                  <span className="text-sm">{action.label}</span>
                  {action.shortcut && (
                    <kbd className="ml-auto">{action.shortcut}</kbd>
                  )}
                </PaletteItem>
              ))}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function PaletteItem({
  isSelected,
  onClick,
  children,
}: {
  isSelected: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      className="w-full flex items-center gap-2.5 px-4 py-2 text-left cursor-pointer"
      style={{
        backgroundColor: isSelected ? "var(--color-accent-muted)" : "transparent",
        color: isSelected ? "var(--color-text-primary)" : "var(--color-text-secondary)",
      }}
      onMouseEnter={(e) => {
        if (!isSelected) e.currentTarget.style.backgroundColor = "var(--color-hover)";
      }}
      onMouseLeave={(e) => {
        if (!isSelected) e.currentTarget.style.backgroundColor = "transparent";
      }}
      onClick={onClick}
      role="option"
      aria-selected={isSelected}
    >
      {children}
    </button>
  );
}

function SectionHeader({ children }: { children: React.ReactNode }) {
  return (
    <div
      className="px-4 py-1.5 text-[10px] font-semibold uppercase tracking-widest"
      style={{ color: "var(--color-text-tertiary)" }}
    >
      {children}
    </div>
  );
}

function SearchIcon() {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="none"
      stroke="var(--color-text-tertiary)"
      strokeWidth="1.5"
      strokeLinecap="round"
    >
      <circle cx="7" cy="7" r="5" />
      <line x1="11" y1="11" x2="14" y2="14" />
    </svg>
  );
}

/** Add a node ID to the recent list in localStorage. */
function addToRecent(nodeId: string): void {
  try {
    const stored = window.localStorage.getItem(RECENT_KEY);
    const recent: string[] = stored ? (JSON.parse(stored) as string[]) : [];
    const updated = [nodeId, ...recent.filter((id) => id !== nodeId)].slice(
      0,
      MAX_RECENT,
    );
    window.localStorage.setItem(RECENT_KEY, JSON.stringify(updated));
  } catch {
    // Ignore storage errors.
  }
}
