/**
 * Navigation context — manages which view is active and which node is selected.
 * This is the glue between sidebar navigation, main content, and command palette.
 */

import { createContext, useCallback, useContext, useEffect, useState } from "react";
import { isValidNodeId } from "../utils/nodeId";
import { deselectedPath, parseTicketLocation, ticketPath, type TicketLocation } from "../utils/ticketLocation";

/** Available views in the main content area. */
export type ViewType = "all-issues" | "node-detail" | "dashboard" | "stale" | "agents" | "kanban";

interface NavigationState {
  /** Current active view. */
  view: ViewType;
  /** Currently selected node ID (for node-detail view). */
  selectedNodeId: string | null;
  /** True when the URL names a ticket whose id failed validation (never fetched). */
  invalidTicketRef?: boolean;
  /** Navigate to a specific view. */
  navigateTo: (view: ViewType) => void;
  /** Navigate to a node detail view. */
  selectNode: (nodeId: string) => void;
  /** Go back to the previous view (all-issues). */
  goBack: () => void;
}

const NavigationContext = createContext<NavigationState | null>(null);

export function NavigationProvider({ children }: { children: React.ReactNode }) {
  const [loc, setLoc] = useState<TicketLocation>(() =>
    parseTicketLocation(window.location.pathname, window.location.search));
  const [view, setView] = useState<ViewType>(() => (loc.kind === "none" ? "all-issues" : "node-detail"));
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(() => (loc.kind === "ticket" ? loc.id : null));
  const [invalidTicketRef, setInvalid] = useState<boolean>(() => loc.kind === "invalid");

  const applyLocation = useCallback((l: TicketLocation) => {
    setLoc(l);
    setSelectedNodeId(l.kind === "ticket" ? l.id : null);
    setInvalid(l.kind === "invalid");
    setView(l.kind === "none" ? "all-issues" : "node-detail");
  }, []);

  // Rewrite the ?node alias to the canonical path; follow back/forward.
  useEffect(() => {
    if (loc.kind === "ticket" && loc.alias) {
      window.history.replaceState(null, "", ticketPath(loc.id, window.location.search));
    }
    const onPop = () => applyLocation(parseTicketLocation(window.location.pathname, window.location.search));
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, [loc, applyLocation]);

  const leaveTicket = useCallback(() => {
    setInvalid(false);
    if (window.location.pathname.startsWith("/ticket/")) {
      window.history.pushState(null, "", deselectedPath(window.location.search));
    } else if (new URLSearchParams(window.location.search).has("node")) {
      // A rejected ?node alias is dropped from the address, other parameters kept.
      window.history.replaceState(null, "", deselectedPath(window.location.search));
    }
  }, []);

  const navigateTo = useCallback((v: ViewType) => {
    setView(v);
    if (v !== "node-detail") {
      setSelectedNodeId(null);
      leaveTicket();
    }
  }, [leaveTicket]);

  const goBack = useCallback(() => {
    setView("all-issues");
    setSelectedNodeId(null);
    leaveTicket();
  }, [leaveTicket]);

  const selectNode = useCallback((nodeId: string) => {
    // An empty or malformed id (the not-found "Go back" button passes "") is a deselect.
    if (!isValidNodeId(nodeId)) {
      goBack();
      return;
    }
    setSelectedNodeId(nodeId);
    setInvalid(false);
    setView("node-detail");
    const target = ticketPath(nodeId, window.location.search);
    if (target !== window.location.pathname + window.location.search) {
      window.history.pushState(null, "", target);
    }
  }, [goBack]);

  return (
    <NavigationContext.Provider value={{ view, selectedNodeId, invalidTicketRef, navigateTo, selectNode, goBack }}>
      {children}
    </NavigationContext.Provider>
  );
}

// Intentional Provider+hook colocation (idiomatic React context pattern).
// react-refresh is a dev-HMR-only rule with no runtime/correctness impact;
// splitting the hook into its own module would fragment every consumer's
// imports for no benefit. Scoped disable rather than a blanket one.
// eslint-disable-next-line react-refresh/only-export-components
export function useNavigation(): NavigationState {
  const ctx = useContext(NavigationContext);
  if (!ctx) throw new Error("useNavigation must be used within NavigationProvider");
  return ctx;
}
