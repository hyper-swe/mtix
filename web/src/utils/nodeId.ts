/**
 * Node-id helpers shared across the UI per FR-MULTI-PROJECT.
 *
 * mtix ids embed their project prefix: `splitID` (Go side) cuts at the
 * last dash before the first dot, so `MTIX-DEV-OPS-1.2.3` parses to
 * prefix `MTIX-DEV-OPS`, root `1`, path `2.3`. These helpers mirror that
 * parsing on the client.
 */

/** Prefix grammar per the FR: leading uppercase, then up to 19 of A-Z0-9-. */
export const PREFIX_RE = /^[A-Z][A-Z0-9-]{0,19}$/;

/**
 * Extract the project prefix from a node id.
 *
 * The prefix is everything before the last dash that precedes the first dot
 * (the numeric root). Returns "" when no dash is present — e.g. a bare relative
 * id like "1.2" carries no project prefix of its own.
 */
export function projectFromId(id: string): string {
  if (!id) return "";
  const firstDot = id.indexOf(".");
  const head = firstDot === -1 ? id : id.slice(0, firstDot);
  const lastDash = head.lastIndexOf("-");
  if (lastDash === -1) return "";
  return head.slice(0, lastDash);
}

/**
 * Short display form of a dot-notation id: the trailing `.N` segment for
 * nested nodes, or the full id for roots (`PREFIX-N`).
 */
export function shortId(id: string): string {
  const parts = id.split(".");
  if (parts.length <= 2) return id;
  return "." + parts[parts.length - 1];
}

/** Validate a project prefix against the grammar. */
export function isValidPrefix(prefix: string): boolean {
  return PREFIX_RE.test(prefix);
}
