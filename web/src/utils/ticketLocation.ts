/**
 * Per-ticket deep link helpers (MTIX-230).
 *
 * The canonical URL for a ticket is /ticket/<ID>; ?node=<ID> is accepted as
 * an alias and rewritten. The id is hostile input: it is only ever returned
 * as a usable id after it passes isValidNodeId, so nothing downstream fetches
 * or renders an unvalidated value.
 */

import { isValidNodeId } from "./nodeId";

const TICKET_PREFIX = "/ticket/";

/** Result of reading window.location. */
export type TicketLocation =
  | { kind: "none" }
  | { kind: "invalid" }
  | { kind: "ticket"; id: string; alias: boolean };

function decodeSegment(raw: string): string | null {
  try {
    return decodeURIComponent(raw);
  } catch {
    return null;
  }
}

/** Parse a pathname and search string into a ticket location. */
export function parseTicketLocation(pathname: string, search: string): TicketLocation {
  if (pathname.startsWith(TICKET_PREFIX)) {
    const id = decodeSegment(pathname.slice(TICKET_PREFIX.length));
    return id !== null && isValidNodeId(id) ? { kind: "ticket", id, alias: false } : { kind: "invalid" };
  }
  const alias = new URLSearchParams(search).get("node");
  if (alias !== null) {
    return isValidNodeId(alias) ? { kind: "ticket", id: alias, alias: true } : { kind: "invalid" };
  }
  return { kind: "none" };
}

/** Query string without the node alias, "" or "?a=b". */
function filterQuery(search: string): string {
  const params = new URLSearchParams(search);
  params.delete("node");
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

/** Canonical path for a validated ticket id, keeping the filter query. */
export function ticketPath(id: string, search: string): string {
  return `${TICKET_PREFIX}${encodeURIComponent(id)}${filterQuery(search)}`;
}

/** Path after deselecting a ticket: root, filters kept. */
export function deselectedPath(search: string): string {
  return `/${filterQuery(search)}`;
}
