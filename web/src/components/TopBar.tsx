import { useState } from "react";
import { useWebSocket } from "../contexts/WebSocketContext";
import { useTheme } from "../contexts/ThemeContext";

interface TopBarProps {
  onToggleSidebar: () => void;
  sidebarCollapsed: boolean;
  onOpenPalette?: () => void;
  onCreateNode?: () => void;
}

/**
 * Top bar with project selector, Cmd+K search trigger,
 * create button, and connection status per FR-UI-2.
 */
export function TopBar({ onToggleSidebar, sidebarCollapsed, onOpenPalette, onCreateNode }: TopBarProps) {
  const { status } = useWebSocket();
  const { theme, toggleTheme, density, toggleDensity } = useTheme();

  return (
    <header
      className="flex items-center px-3 flex-shrink-0"
      style={{
        height: density === "compact" ? 36 : 44,
        backgroundColor: "var(--color-surface)",
        borderBottom: "1px solid var(--color-border)",
      }}
    >
      {/* Sidebar toggle */}
      <IconButton
        onClick={onToggleSidebar}
        ariaLabel={sidebarCollapsed ? "Expand sidebar" : "Collapse sidebar"}
        tooltip={sidebarCollapsed ? "Expand sidebar  [" : "Collapse sidebar  ["}
      >
        <SidebarIcon collapsed={sidebarCollapsed} />
      </IconButton>

      {/* Logo */}
      <span
        className="font-semibold text-sm ml-2 mr-3 tracking-tight select-none"
        style={{ color: "var(--color-accent)" }}
      >
        mtix
      </span>

      {/* Project selector */}
      <button
        className="text-xs px-2 py-1 rounded mr-auto cursor-pointer"
        style={{
          color: "var(--color-text-secondary)",
          border: "1px solid var(--color-border)",
          borderRadius: "var(--radius-md)",
        }}
        onMouseEnter={(e) => (e.currentTarget.style.backgroundColor = "var(--color-hover)")}
        onMouseLeave={(e) => (e.currentTarget.style.backgroundColor = "transparent")}
      >
        Select Project
      </button>

      {/* Create button */}
      {onCreateNode && (
        <button
          className="flex items-center gap-1.5 text-xs px-2.5 py-1 rounded cursor-pointer mr-2"
          style={{
            color: "#ffffff",
            backgroundColor: "var(--color-accent)",
            borderRadius: "var(--radius-md)",
          }}
          onMouseEnter={(e) => (e.currentTarget.style.backgroundColor = "var(--color-accent-hover)")}
          onMouseLeave={(e) => (e.currentTarget.style.backgroundColor = "var(--color-accent)")}
          onClick={onCreateNode}
          aria-label="Create issue (c)"
          title="Create issue (c)"
          data-testid="create-button"
        >
          <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
            <line x1="6" y1="2" x2="6" y2="10" />
            <line x1="2" y1="6" x2="10" y2="6" />
          </svg>
          <span className="hidden sm:inline">New</span>
        </button>
      )}

      {/* Cmd+K search */}
      <button
        className="flex items-center gap-1.5 text-xs px-2.5 py-1 rounded cursor-pointer mr-2"
        style={{
          color: "var(--color-text-tertiary)",
          border: "1px solid var(--color-border)",
          borderRadius: "var(--radius-md)",
        }}
        onMouseEnter={(e) => (e.currentTarget.style.backgroundColor = "var(--color-hover)")}
        onMouseLeave={(e) => (e.currentTarget.style.backgroundColor = "transparent")}
        aria-label="Search (Cmd+K)"
        onClick={onOpenPalette}
      >
        <SearchIcon />
        <span className="hidden sm:inline">Search</span>
        <kbd className="hidden sm:inline">{"\u2318"}K</kbd>
      </button>

      {/* Density toggle */}
      <IconButton
        onClick={toggleDensity}
        ariaLabel={`Switch to ${density === "comfortable" ? "compact" : "comfortable"} density`}
        tooltip={density === "comfortable" ? "Compact density  \u2318D" : "Comfortable density  \u2318D"}
      >
        <DensityIcon compact={density === "compact"} />
      </IconButton>

      {/* Theme toggle */}
      <IconButton
        onClick={toggleTheme}
        ariaLabel={`Switch to ${theme === "dark" ? "light" : "dark"} theme`}
        tooltip={theme === "dark" ? "Light theme" : "Dark theme"}
        className="mr-2"
      >
        {theme === "dark" ? <SunIcon /> : <MoonIcon />}
      </IconButton>

      {/* Connection status */}
      <ConnectionIndicator status={status} />
    </header>
  );
}

function ConnectionIndicator({
  status,
}: {
  status: "connected" | "reconnecting" | "disconnected";
}) {
  const config: Record<string, { color: string; label: string; pulse: boolean }> = {
    connected: { color: "var(--color-status-done)", label: "Connected", pulse: false },
    reconnecting: { color: "var(--color-status-invalidated)", label: "Reconnecting", pulse: true },
    disconnected: { color: "var(--color-status-blocked)", label: "Disconnected", pulse: false },
  };
  const c = config[status] ?? config["disconnected"]!;

  return (
    <div className="flex items-center gap-1.5" title={`WebSocket: ${status}`}>
      <div
        className="w-1.5 h-1.5 rounded-full"
        style={{
          backgroundColor: c!.color,
          animation: c!.pulse ? "pulse-dot 2s ease-in-out infinite" : "none",
        }}
      />
      <span
        className="text-xs hidden sm:inline"
        style={{ color: "var(--color-text-tertiary)" }}
      >
        {c!.label}
      </span>
    </div>
  );
}

/** Reusable icon button with tooltip. */
function IconButton({
  onClick,
  ariaLabel,
  tooltip,
  className = "",
  children,
}: {
  onClick: () => void;
  ariaLabel: string;
  tooltip: string;
  className?: string;
  children: React.ReactNode;
}) {
  const [showTooltip, setShowTooltip] = useState(false);

  return (
    <div className={`relative inline-flex ${className}`}>
      <button
        onClick={onClick}
        className="p-1.5 rounded cursor-pointer"
        style={{ color: "var(--color-text-tertiary)" }}
        onMouseEnter={(e) => {
          e.currentTarget.style.backgroundColor = "var(--color-hover)";
          setShowTooltip(true);
        }}
        onMouseLeave={(e) => {
          e.currentTarget.style.backgroundColor = "transparent";
          setShowTooltip(false);
        }}
        aria-label={ariaLabel}
      >
        {children}
      </button>
      {showTooltip && (
        <div
          className="absolute top-full left-1/2 -translate-x-1/2 mt-1.5 px-2 py-1 rounded text-[11px] whitespace-nowrap z-50 pointer-events-none"
          style={{
            backgroundColor: "var(--color-surface-overlay)",
            color: "var(--color-text-primary)",
            border: "1px solid var(--color-border)",
            boxShadow: "var(--shadow-md)",
          }}
        >
          {tooltip}
        </div>
      )}
    </div>
  );
}

function SidebarIcon({ collapsed }: { collapsed: boolean }) {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
      {collapsed ? (
        <>
          <rect x="1" y="2" width="14" height="12" rx="2" />
          <line x1="5" y1="2" x2="5" y2="14" />
        </>
      ) : (
        <>
          <rect x="1" y="2" width="14" height="12" rx="2" />
          <line x1="6" y1="2" x2="6" y2="14" />
        </>
      )}
    </svg>
  );
}

function SearchIcon() {
  return (
    <svg width="13" height="13" viewBox="0 0 13 13" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
      <circle cx="5.5" cy="5.5" r="4" />
      <line x1="8.5" y1="8.5" x2="12" y2="12" />
    </svg>
  );
}

function SunIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
      <circle cx="8" cy="8" r="3" />
      <line x1="8" y1="1" x2="8" y2="3" />
      <line x1="8" y1="13" x2="8" y2="15" />
      <line x1="1" y1="8" x2="3" y2="8" />
      <line x1="13" y1="8" x2="15" y2="8" />
    </svg>
  );
}

function MoonIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
      <path d="M13 9A6 6 0 1 1 7 3a5 5 0 0 0 6 6z" />
    </svg>
  );
}

function DensityIcon({ compact }: { compact: boolean }) {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
      {compact ? (
        <>
          <line x1="2" y1="4" x2="14" y2="4" />
          <line x1="2" y1="8" x2="14" y2="8" />
          <line x1="2" y1="12" x2="14" y2="12" />
        </>
      ) : (
        <>
          <line x1="2" y1="3" x2="14" y2="3" />
          <line x1="2" y1="8" x2="14" y2="8" />
          <line x1="2" y1="13" x2="14" y2="13" />
        </>
      )}
    </svg>
  );
}
