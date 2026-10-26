import { useNavigation } from "../contexts/NavigationContext";
import { TreeView } from "./TreeView";
import type { useNodeStore } from "../hooks/useNodeStore";

interface SidebarProps {
  nodeStore: ReturnType<typeof useNodeStore>;
  onClose?: () => void;
}

/**
 * Sidebar component per FR-UI-2.
 * Contains tree navigation, views, and filters.
 */
export function Sidebar({ nodeStore, onClose }: SidebarProps) {
  const { view, navigateTo, selectNode } = useNavigation();

  const treeItems = nodeStore.flatTree();

  const handleSelectNode = (nodeId: string) => {
    nodeStore.selectNode(nodeId);
    selectNode(nodeId);
    onClose?.();
  };

  return (
    <aside
      className="h-full flex flex-col overflow-hidden"
      style={{ backgroundColor: "var(--color-surface)" }}
    >
      {/* Mobile close button */}
      {onClose && (
        <div className="flex justify-end p-2">
          <button
            onClick={onClose}
            className="p-1 rounded cursor-pointer"
            style={{ color: "var(--color-text-tertiary)" }}
            onMouseEnter={(e) => (e.currentTarget.style.backgroundColor = "var(--color-hover)")}
            onMouseLeave={(e) => (e.currentTarget.style.backgroundColor = "transparent")}
            aria-label="Close sidebar"
          >
            <CloseIcon />
          </button>
        </div>
      )}

      {/* Views */}
      <div className="px-3 py-2">
        <SectionLabel>Views</SectionLabel>
        <NavItem
          icon={<ListIcon />}
          label="All Issues"
          active={view === "all-issues"}
          onClick={() => navigateTo("all-issues")}
        />
        <NavItem
          icon={<DashboardIcon />}
          label="Dashboard"
          active={view === "dashboard"}
          onClick={() => navigateTo("dashboard")}
        />
        <NavItem
          icon={<AgentIcon />}
          label="Agent Activity"
          active={view === "agents"}
          onClick={() => navigateTo("agents")}
        />
        <NavItem
          icon={<KanbanIcon />}
          label="Kanban Board"
          active={view === "kanban"}
          onClick={() => navigateTo("kanban")}
        />
        <NavItem
          icon={<StaleIcon />}
          label="Stale Board"
          active={view === "stale"}
          onClick={() => navigateTo("stale")}
        />
      </div>

      <Divider />

      {/* Tree navigation */}
      <div className="px-3 py-2 flex-1 min-h-0 flex flex-col">
        <div className="flex items-center justify-between mb-1">
          <SectionLabel>Tree</SectionLabel>
          <button
            className="text-[10px] px-1.5 py-0.5 rounded cursor-pointer"
            style={{
              color: nodeStore.hideDone ? "var(--color-accent)" : "var(--color-text-tertiary)",
              backgroundColor: nodeStore.hideDone ? "var(--color-accent-muted)" : "transparent",
            }}
            onClick={() => nodeStore.setHideDone(!nodeStore.hideDone)}
            title={nodeStore.hideDone ? "Show completed tasks" : "Hide completed tasks"}
          >
            {nodeStore.hideDone ? "Show done" : "Hide done"}
          </button>
        </div>
        {treeItems.length === 0 ? (
          <div
            className="text-xs py-3 px-1"
            style={{ color: "var(--color-text-tertiary)" }}
          >
            {nodeStore.loading ? "Loading..." : "No nodes yet"}
          </div>
        ) : (
          <TreeView
            items={treeItems}
            selectedId={nodeStore.selectedId}
            onSelect={handleSelectNode}
            onToggleExpand={nodeStore.toggleExpand}
          />
        )}
      </div>
    </aside>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div
      className="text-[10px] font-semibold uppercase tracking-widest px-2"
      style={{ color: "var(--color-text-tertiary)" }}
    >
      {children}
    </div>
  );
}

function NavItem({
  icon,
  label,
  active = false,
  onClick,
}: {
  icon: React.ReactNode;
  label: string;
  active?: boolean;
  onClick?: () => void;
}) {
  return (
    <button
      className="w-full flex items-center gap-2 text-left text-sm px-2 py-1.5 rounded cursor-pointer"
      style={{
        color: active ? "var(--color-text-primary)" : "var(--color-text-secondary)",
        backgroundColor: active ? "var(--color-accent-muted)" : "transparent",
        borderRadius: "var(--radius-md)",
        fontWeight: active ? 500 : 400,
      }}
      onMouseEnter={(e) => {
        if (!active) e.currentTarget.style.backgroundColor = "var(--color-hover)";
      }}
      onMouseLeave={(e) => {
        if (!active) e.currentTarget.style.backgroundColor = "transparent";
      }}
      onClick={onClick}
    >
      <span style={{ color: active ? "var(--color-accent)" : "var(--color-text-tertiary)" }}>
        {icon}
      </span>
      {label}
    </button>
  );
}

function Divider() {
  return (
    <hr
      className="mx-3 my-1 border-0 h-px"
      style={{ backgroundColor: "var(--color-border)" }}
    />
  );
}

function CloseIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
      <line x1="4" y1="4" x2="12" y2="12" />
      <line x1="12" y1="4" x2="4" y2="12" />
    </svg>
  );
}

function ListIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round">
      <line x1="4" y1="3" x2="12" y2="3" />
      <line x1="4" y1="7" x2="12" y2="7" />
      <line x1="4" y1="11" x2="12" y2="11" />
      <circle cx="2" cy="3" r="0.5" fill="currentColor" stroke="none" />
      <circle cx="2" cy="7" r="0.5" fill="currentColor" stroke="none" />
      <circle cx="2" cy="11" r="0.5" fill="currentColor" stroke="none" />
    </svg>
  );
}

function DashboardIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round">
      <rect x="1" y="1" width="5" height="5" rx="1" />
      <rect x="8" y="1" width="5" height="3" rx="1" />
      <rect x="1" y="8" width="5" height="5" rx="1" />
      <rect x="8" y="6" width="5" height="7" rx="1" />
    </svg>
  );
}

function AgentIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round">
      <circle cx="7" cy="5" r="3" />
      <path d="M2 13c0-2.8 2.2-5 5-5s5 2.2 5 5" />
    </svg>
  );
}

function KanbanIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round">
      <rect x="1" y="1" width="3" height="12" rx="0.5" />
      <rect x="5.5" y="1" width="3" height="8" rx="0.5" />
      <rect x="10" y="1" width="3" height="10" rx="0.5" />
    </svg>
  );
}

function StaleIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round">
      <circle cx="7" cy="7" r="5.5" />
      <line x1="7" y1="4" x2="7" y2="7.5" />
      <line x1="7" y1="7.5" x2="9.5" y2="9" />
    </svg>
  );
}
