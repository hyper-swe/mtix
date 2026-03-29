/**
 * Navigation context — manages which view is active and which node is selected.
 * This is the glue between sidebar navigation, main content, and command palette.
 */

import { createContext, useCallback, useContext, useState } from "react";

/** Available views in the main content area. */
export type ViewType = "all-issues" | "node-detail" | "dashboard" | "stale" | "agents" | "kanban";

interface NavigationState {
  /** Current active view. */
  view: ViewType;
  /** Currently selected node ID (for node-detail view). */
  selectedNodeId: string | null;
  /** Navigate to a specific view. */
  navigateTo: (view: ViewType) => void;
  /** Navigate to a node detail view. */
  selectNode: (nodeId: string) => void;
  /** Go back to the previous view (all-issues). */
  goBack: () => void;
}

const NavigationContext = createContext<NavigationState | null>(null);

export function NavigationProvider({ children }: { children: React.ReactNode }) {
  const [view, setView] = useState<ViewType>("all-issues");
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);

  const navigateTo = useCallback((v: ViewType) => {
    setView(v);
    if (v !== "node-detail") {
      setSelectedNodeId(null);
    }
  }, []);

  const selectNode = useCallback((nodeId: string) => {
    setSelectedNodeId(nodeId);
    setView("node-detail");
  }, []);

  const goBack = useCallback(() => {
    setView("all-issues");
    setSelectedNodeId(null);
  }, []);

  return (
    <NavigationContext.Provider value={{ view, selectedNodeId, navigateTo, selectNode, goBack }}>
      {children}
    </NavigationContext.Provider>
  );
}

export function useNavigation(): NavigationState {
  const ctx = useContext(NavigationContext);
  if (!ctx) throw new Error("useNavigation must be used within NavigationProvider");
  return ctx;
}
