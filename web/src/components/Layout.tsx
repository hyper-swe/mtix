import { useCallback, useEffect, useRef, useState } from "react";
import { TopBar } from "./TopBar";
import { Sidebar } from "./Sidebar";
import { MainContent } from "./MainContent";
import { Breadcrumb } from "./Breadcrumb";
import { CommandPalette } from "./CommandPalette";
import { CreateNodeModal } from "./CreateNodeModal";
import { useNavigation } from "../contexts/NavigationContext";
import { useNodeStore } from "../hooks/useNodeStore";

/** Minimum sidebar width in pixels. */
const MIN_SIDEBAR_WIDTH = 200;

/** Maximum sidebar width in pixels. */
const MAX_SIDEBAR_WIDTH = 480;

/** Default sidebar width in pixels. */
const DEFAULT_SIDEBAR_WIDTH = 260;

/** Storage key for persisted sidebar width. */
const SIDEBAR_WIDTH_KEY = "mtix-sidebar-width";

/** Storage key for persisted sidebar collapsed state. */
const SIDEBAR_COLLAPSED_KEY = "mtix-sidebar-collapsed";

/** Mobile breakpoint in pixels per FR-UI-2. */
const MOBILE_BREAKPOINT = 768;

/** Load persisted sidebar width. */
function loadSidebarWidth(): number {
  const stored = window.localStorage.getItem(SIDEBAR_WIDTH_KEY);
  if (stored) {
    const parsed = parseInt(stored, 10);
    if (!isNaN(parsed) && parsed >= MIN_SIDEBAR_WIDTH && parsed <= MAX_SIDEBAR_WIDTH) {
      return parsed;
    }
  }
  return DEFAULT_SIDEBAR_WIDTH;
}

/** Load persisted sidebar collapsed state. */
function loadSidebarCollapsed(): boolean {
  return window.localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === "true";
}

/**
 * Two-panel layout per FR-UI-2 and requirement-ui.md section 2.
 * Left: collapsible, resizable sidebar. Right: main content.
 * Bottom: breadcrumb bar per FR-UI-10.
 */
export function Layout() {
  const [sidebarWidth, setSidebarWidth] = useState(loadSidebarWidth);
  const [collapsed, setCollapsed] = useState(loadSidebarCollapsed);
  const [isMobile, setIsMobile] = useState(
    () => window.innerWidth < MOBILE_BREAKPOINT,
  );
  const [mobileDrawerOpen, setMobileDrawerOpen] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const isResizing = useRef(false);

  const { selectNode } = useNavigation();
  const nodeStore = useNodeStore();

  // Load root nodes on mount.
  useEffect(() => {
    nodeStore.loadRoots();
  }, []);

  // Track viewport size for mobile breakpoint.
  useEffect(() => {
    const handler = () => {
      setIsMobile(window.innerWidth < MOBILE_BREAKPOINT);
    };
    window.addEventListener("resize", handler);
    return () => window.removeEventListener("resize", handler);
  }, []);

  // Keyboard shortcuts.
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement)?.tagName;
      const isInput = tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";

      // Cmd+K / Ctrl+K — command palette (works even in inputs)
      if (e.key === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setPaletteOpen((prev) => !prev);
        return;
      }

      if (isInput) return;

      // [ — toggle sidebar
      if (e.key === "[" && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault();
        toggleSidebar();
      }

      // c — create node
      if (e.key === "c" && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault();
        setCreateModalOpen(true);
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []);

  const toggleSidebar = useCallback(() => {
    setCollapsed((prev) => {
      const next = !prev;
      window.localStorage.setItem(SIDEBAR_COLLAPSED_KEY, String(next));
      return next;
    });
  }, []);

  // Drag handle for resizing sidebar.
  const startResize = useCallback(
    (e: React.MouseEvent) => {
      e.preventDefault();
      isResizing.current = true;

      const startX = e.clientX;
      const startWidth = sidebarWidth;

      const onMouseMove = (moveEvent: MouseEvent) => {
        if (!isResizing.current) return;
        const delta = moveEvent.clientX - startX;
        const newWidth = Math.min(
          MAX_SIDEBAR_WIDTH,
          Math.max(MIN_SIDEBAR_WIDTH, startWidth + delta),
        );
        setSidebarWidth(newWidth);
      };

      const onMouseUp = () => {
        isResizing.current = false;
        window.localStorage.setItem(SIDEBAR_WIDTH_KEY, String(sidebarWidth));
        document.removeEventListener("mousemove", onMouseMove);
        document.removeEventListener("mouseup", onMouseUp);
      };

      document.addEventListener("mousemove", onMouseMove);
      document.addEventListener("mouseup", onMouseUp);
    },
    [sidebarWidth],
  );

  // Persist sidebar width when resizing stops.
  useEffect(() => {
    if (!isResizing.current) {
      window.localStorage.setItem(SIDEBAR_WIDTH_KEY, String(sidebarWidth));
    }
  }, [sidebarWidth]);

  const handlePaletteSelect = useCallback(
    (nodeId: string) => {
      selectNode(nodeId);
      setPaletteOpen(false);
    },
    [selectNode],
  );

  const handlePaletteAction = useCallback((action: string) => {
    if (action === "create-node") {
      setCreateModalOpen(true);
    }
  }, []);

  const handleNodeCreated = useCallback(
    (node: { id: string; title: string }) => {
      nodeStore.loadRoots();
      selectNode(node.id);
    },
    [nodeStore, selectNode],
  );

  return (
    <div className="flex flex-col h-screen overflow-hidden">
      <TopBar
        onToggleSidebar={toggleSidebar}
        sidebarCollapsed={collapsed}
        onOpenPalette={() => setPaletteOpen(true)}
        onCreateNode={() => setCreateModalOpen(true)}
      />

      <div className="flex flex-1 min-h-0">
        {/* Sidebar — desktop: inline panel, mobile: drawer overlay */}
        {isMobile ? (
          <>
            {mobileDrawerOpen && (
              <div
                className="fixed inset-0 z-40"
                style={{ backgroundColor: "rgba(0, 0, 0, 0.4)" }}
                onClick={() => setMobileDrawerOpen(false)}
                aria-hidden="true"
              />
            )}
            <div
              className={`fixed inset-y-0 left-0 z-50 transition-transform duration-200 ${
                mobileDrawerOpen
                  ? "translate-x-0"
                  : "-translate-x-full"
              }`}
              style={{ width: DEFAULT_SIDEBAR_WIDTH }}
            >
              <Sidebar
                nodeStore={nodeStore}
                onClose={() => setMobileDrawerOpen(false)}
              />
            </div>
          </>
        ) : (
          !collapsed && (
            <>
              <div
                className="flex-shrink-0"
                style={{
                  width: sidebarWidth,
                  borderRight: "1px solid var(--color-border)",
                }}
              >
                <Sidebar nodeStore={nodeStore} />
              </div>
              {/* Drag handle for resizing */}
              <div
                className="w-1 cursor-col-resize flex-shrink-0"
                style={{ backgroundColor: "transparent" }}
                onMouseDown={startResize}
                onMouseEnter={(e) => (e.currentTarget.style.backgroundColor = "var(--color-accent-muted)")}
                onMouseLeave={(e) => (e.currentTarget.style.backgroundColor = "transparent")}
                role="separator"
                aria-orientation="vertical"
                aria-label="Resize sidebar"
              />
            </>
          )
        )}

        {/* Main content area */}
        <div className="flex-1 min-w-0 overflow-auto">
          <MainContent nodeStore={nodeStore} />
        </div>
      </div>

      <Breadcrumb />

      {/* Command Palette */}
      <CommandPalette
        isOpen={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        onSelectNode={handlePaletteSelect}
        onAction={handlePaletteAction}
      />

      {/* Create Node Modal */}
      <CreateNodeModal
        isOpen={createModalOpen}
        onClose={() => setCreateModalOpen(false)}
        onCreated={handleNodeCreated}
      />

      {/* Mobile menu button */}
      {isMobile && (
        <button
          className="fixed bottom-4 left-4 z-30 w-10 h-10 rounded-full flex items-center justify-center"
          style={{
            backgroundColor: "var(--color-accent)",
            color: "white",
            boxShadow: "var(--shadow-lg)",
          }}
          onClick={() => setMobileDrawerOpen(true)}
          aria-label="Open sidebar"
        >
          <MenuIcon />
        </button>
      )}
    </div>
  );
}

function MenuIcon() {
  return (
    <svg width="18" height="18" viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
      <line x1="3" y1="5" x2="17" y2="5" />
      <line x1="3" y1="10" x2="17" y2="10" />
      <line x1="3" y1="15" x2="17" y2="15" />
    </svg>
  );
}
