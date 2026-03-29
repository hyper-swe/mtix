interface FilterChipsProps {
  chips: Array<{ label: string; onRemove: () => void }>;
  onClearAll: () => void;
}

/**
 * Active filter chips displayed above the node list.
 * Each chip is removable; a "Clear all" button resets everything.
 */
export function FilterChips({ chips, onClearAll }: FilterChipsProps) {
  if (chips.length === 0) return null;

  return (
    <div className="flex flex-wrap gap-1.5 px-4 py-2 border-b" style={{ borderColor: "var(--color-border)" }}>
      {chips.map((chip) => (
        <span
          key={chip.label}
          className="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded-full"
          style={{
            backgroundColor: "var(--color-border)",
            color: "var(--color-text-primary)",
          }}
        >
          {chip.label}
          <button
            onClick={chip.onRemove}
            className="ml-0.5 hover:opacity-70"
            aria-label={`Remove filter: ${chip.label}`}
          >
            {"\u00D7"}
          </button>
        </span>
      ))}
      <button
        onClick={onClearAll}
        className="text-xs hover:underline"
        style={{ color: "var(--color-accent)" }}
      >
        Clear all
      </button>
    </div>
  );
}
