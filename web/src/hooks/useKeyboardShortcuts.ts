/**
 * Keyboard shortcut handler per requirement-ui.md §1.
 * Registers global keyboard shortcuts that do NOT fire in text inputs.
 */

import { useCallback, useEffect, useRef } from "react";

/** A keyboard shortcut definition. */
export interface Shortcut {
  /** Key to match (e.g., "j", "k", "Enter", "Escape"). */
  key: string;
  /** Whether Cmd/Ctrl must be held. */
  meta?: boolean;
  /** Whether Shift must be held. */
  shift?: boolean;
  /** Human-readable description. */
  description: string;
  /** Handler function. */
  handler: () => void;
}

/** Tags that should suppress shortcuts. */
const INPUT_TAGS = new Set(["INPUT", "TEXTAREA", "SELECT"]);

/** Check if the event target is a text input. */
function isInputFocused(e: KeyboardEvent): boolean {
  const target = e.target as HTMLElement;
  if (!target) return false;
  if (INPUT_TAGS.has(target.tagName)) return true;
  if (target.contentEditable === "true") return true;
  return false;
}

/**
 * Hook to register keyboard shortcuts.
 * Shortcuts are suppressed when focus is in text inputs.
 *
 * @param shortcuts Array of shortcut definitions.
 * @param enabled Whether shortcuts are active (default: true).
 */
export function useKeyboardShortcuts(
  shortcuts: Shortcut[],
  enabled = true,
): void {
  // Use ref to avoid re-registering on every render.
  const shortcutsRef = useRef(shortcuts);
  shortcutsRef.current = shortcuts;

  const handler = useCallback(
    (e: KeyboardEvent) => {
      if (!enabled) return;

      // Don't fire in text inputs.
      if (isInputFocused(e)) return;

      for (const shortcut of shortcutsRef.current) {
        const metaMatch = shortcut.meta
          ? e.metaKey || e.ctrlKey
          : !e.metaKey && !e.ctrlKey;
        const shiftMatch = shortcut.shift ? e.shiftKey : !e.shiftKey;

        if (e.key === shortcut.key && metaMatch && shiftMatch) {
          e.preventDefault();
          shortcut.handler();
          return;
        }
      }
    },
    [enabled],
  );

  useEffect(() => {
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [handler]);
}

/** Default keyboard shortcuts per requirement-ui.md §1 table. */
export function buildDefaultShortcuts(actions: {
  moveDown: () => void;
  moveUp: () => void;
  expand: () => void;
  collapse: () => void;
  openNode: () => void;
  goBack: () => void;
  cycleFocus: () => void;
  createNode: () => void;
  createMicro: () => void;
  markDone: () => void;
  setInProgress: () => void;
  defer: () => void;
  editTitle: () => void;
  editPrompt: () => void;
  cycleStatus: () => void;
  setPriority: (p: number) => void;
  openCommandPalette: () => void;
  showShortcuts: () => void;
}): Shortcut[] {
  return [
    { key: "j", description: "Move down", handler: actions.moveDown },
    { key: "k", description: "Move up", handler: actions.moveUp },
    { key: "l", description: "Expand tree node", handler: actions.expand },
    { key: "h", description: "Collapse tree node", handler: actions.collapse },
    { key: "Enter", description: "Open/select node", handler: actions.openNode },
    { key: "Escape", description: "Close panel / go back", handler: actions.goBack },
    { key: "Tab", description: "Cycle focus", handler: actions.cycleFocus },
    { key: "c", description: "Create node", handler: actions.createNode },
    { key: "m", description: "Create micro issue", handler: actions.createMicro },
    { key: "x", description: "Mark done", handler: actions.markDone },
    { key: "i", description: "Set in_progress", handler: actions.setInProgress },
    { key: "d", description: "Defer", handler: actions.defer },
    { key: "e", description: "Edit title", handler: actions.editTitle },
    { key: "p", description: "Edit prompt", handler: actions.editPrompt },
    { key: "s", description: "Cycle status", handler: actions.cycleStatus },
    { key: "1", description: "Priority: Critical", handler: () => actions.setPriority(1) },
    { key: "2", description: "Priority: High", handler: () => actions.setPriority(2) },
    { key: "3", description: "Priority: Medium", handler: () => actions.setPriority(3) },
    { key: "4", description: "Priority: Low", handler: () => actions.setPriority(4) },
    { key: "5", description: "Priority: Background", handler: () => actions.setPriority(5) },
    {
      key: "k",
      meta: true,
      description: "Command palette",
      handler: actions.openCommandPalette,
    },
    {
      key: "/",
      meta: true,
      description: "Show shortcuts",
      handler: actions.showShortcuts,
    },
  ];
}
