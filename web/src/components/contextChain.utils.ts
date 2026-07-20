/**
 * Map depth to canonical tier letter, matching Go's NodeTypeForDepth
 * post-MTIX-7 (v0.1.1-beta) Agile convention:
 *   depth 0 -> E (Epic)
 *   depth 1 -> S (Story)
 *   depth 2 -> I (Issue)
 *   depth 3+ -> M (Micro)
 *
 * Lives in its own module (not ContextChain.tsx) so the component file only
 * exports a component (react-refresh/only-export-components).
 */
export function levelIndicator(depth: number): string {
  if (depth === 0) return "E";
  if (depth === 1) return "S";
  if (depth === 2) return "I";
  return "M";
}
