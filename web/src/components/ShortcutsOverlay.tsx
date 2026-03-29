interface ShortcutsOverlayProps {
  isOpen: boolean;
  onClose: () => void;
}

/** All keyboard shortcuts per requirement-ui.md §1. */
const SHORTCUTS = [
  { key: "j / k", description: "Move down / up in list" },
  { key: "l / h", description: "Expand / collapse tree node" },
  { key: "Enter", description: "Open / select node" },
  { key: "Esc", description: "Close panel / go back" },
  { key: "Tab", description: "Cycle focus: Tree \u2192 List \u2192 Detail" },
  { key: "c", description: "Create new node" },
  { key: "m", description: "Create micro issue" },
  { key: "x", description: "Mark done" },
  { key: "i", description: "Set in_progress" },
  { key: "d", description: "Defer" },
  { key: "e", description: "Edit title inline" },
  { key: "p", description: "Edit prompt inline" },
  { key: "s", description: "Cycle status" },
  { key: "1-5", description: "Set priority" },
  { key: "\u2318K", description: "Command palette" },
  { key: "\u2318/", description: "Show shortcuts" },
  { key: "\u2318D", description: "Toggle density" },
  { key: "[", description: "Toggle sidebar" },
];

/**
 * Shortcuts overlay per Cmd+/ trigger.
 */
export function ShortcutsOverlay({ isOpen, onClose }: ShortcutsOverlayProps) {
  if (!isOpen) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center"
      style={{ backgroundColor: "rgba(0, 0, 0, 0.4)" }}
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      role="dialog"
      aria-label="Keyboard shortcuts"
    >
      <div
        className="w-full max-w-md rounded-lg shadow-2xl border p-6"
        style={{
          backgroundColor: "var(--color-surface)",
          borderColor: "var(--color-border)",
        }}
      >
        <div className="flex items-center justify-between mb-4">
          <h2
            className="text-sm font-semibold"
            style={{ color: "var(--color-text-primary)" }}
          >
            Keyboard Shortcuts
          </h2>
          <button
            onClick={onClose}
            className="text-sm"
            style={{ color: "var(--color-text-secondary)" }}
          >
            Esc
          </button>
        </div>

        <div className="space-y-1">
          {SHORTCUTS.map((s) => (
            <div key={s.key} className="flex items-center justify-between py-1">
              <span
                className="text-sm"
                style={{ color: "var(--color-text-primary)" }}
              >
                {s.description}
              </span>
              <kbd
                className="text-xs font-mono px-1.5 py-0.5 rounded border"
                style={{
                  color: "var(--color-text-secondary)",
                  borderColor: "var(--color-border)",
                }}
              >
                {s.key}
              </kbd>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
