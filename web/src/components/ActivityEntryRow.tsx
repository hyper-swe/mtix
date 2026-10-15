/** One activity entry, with literal identities and a safe Markdown body (NFR-5.1). */
import type { ActivityEntry } from "../types";
import { SafeMarkdown } from "./SafeMarkdown";

function relativeTime(isoString: string): string {
  const diffSec = Math.floor((Date.now() - new Date(isoString).getTime()) / 1000);
  if (diffSec < 60) return "just now";
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)} min ago`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
  if (diffSec < 604800) return `${Math.floor(diffSec / 86400)}d ago`;
  return new Date(isoString).toLocaleDateString();
}

function activityIcon(type: string): string {
  switch (type) {
    case "status_change": return "●";
    case "comment": return "💬";
    case "prompt_edit": return "✏";
    case "child_created": return "+";
    case "agent_claim": return "▶";
    case "agent_done": return "✓";
    default: return "·";
  }
}

export function ActivityEntryRow({ entry }: { entry: ActivityEntry }) {
  return (
    <div className="flex items-start gap-2 py-2 border-b last:border-b-0"
      style={{ borderColor: "var(--color-border)" }} data-testid="activity-entry">
      <span className="shrink-0 w-5 text-center text-xs mt-0.5">{activityIcon(entry.type)}</span>
      <div className="flex-1 min-w-0">
        <div className="flex items-baseline gap-2 flex-wrap">
          <span className="text-xs font-medium" data-testid="activity-author"
            style={{ color: entry.author.startsWith("agent-") ? "var(--color-accent)" : "var(--color-text-primary)" }}>
            {entry.author}
          </span>
          {entry.addressee && (
            <span className="text-xs" style={{ color: "var(--color-text-secondary)" }}>
              To <span data-testid="activity-addressee">{entry.addressee}</span>
            </span>
          )}
          <span className="text-xs" style={{ color: "var(--color-text-secondary)" }}
            title={new Date(entry.created_at).toLocaleString()} data-testid="activity-timestamp">
            {relativeTime(entry.created_at)}
          </span>
        </div>
        <div className="text-xs mt-0.5" style={{ color: "var(--color-text-secondary)" }} data-testid="activity-text">
          <SafeMarkdown content={entry.text ?? ""} />
        </div>
      </div>
    </div>
  );
}
