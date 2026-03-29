import { useWebSocket } from "../contexts/WebSocketContext";
import { useNavigation } from "../contexts/NavigationContext";

/**
 * Bottom breadcrumb bar per FR-UI-10.
 * Shows path to selected node + view name + connection status.
 */
export function Breadcrumb() {
  const { status } = useWebSocket();
  const { view, selectedNodeId } = useNavigation();

  const viewLabels: Record<string, string> = {
    "all-issues": "All Issues",
    "node-detail": selectedNodeId ?? "Node Detail",
    dashboard: "Dashboard",
    stale: "Stale Board",
    agents: "Agent Activity",
  };

  const statusConfig: Record<string, { label: string; color: string }> = {
    connected: { label: "Connected", color: "var(--color-status-done)" },
    reconnecting: { label: "Reconnecting...", color: "var(--color-status-invalidated)" },
    disconnected: { label: "Disconnected", color: "var(--color-status-blocked)" },
  };

  const s = statusConfig[status] ?? statusConfig["disconnected"]!;

  return (
    <footer
      className="flex items-center h-7 px-4 text-[11px] flex-shrink-0"
      style={{
        backgroundColor: "var(--color-surface)",
        borderTop: "1px solid var(--color-border)",
        color: "var(--color-text-tertiary)",
      }}
    >
      <span className="mr-auto font-mono">
        {viewLabels[view] ?? "mtix"}
      </span>
      <div className="flex items-center gap-1.5">
        <div
          className="w-1.5 h-1.5 rounded-full"
          style={{ backgroundColor: s!.color }}
        />
        <span>{s!.label}</span>
      </div>
    </footer>
  );
}
