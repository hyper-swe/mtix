/**
 * Shared node-id renderer per FR-MULTI-PROJECT (MP-18, D4).
 *
 * Renders a node id consistently across the app. In the all-projects scope it
 * prepends a small project-prefix badge so ids from different projects are
 * visually distinct; in a single/scoped view it renders the id plainly to
 * avoid noise. Used outside a ProjectProvider it degrades to the plain id.
 */

import type { CSSProperties } from "react";
import { useProjectOptional } from "../contexts/ProjectContext";
import { projectFromId, shortId } from "../utils/nodeId";

interface NodeIDProps {
  /** The full node id (e.g. "MTIX-1.2.3"). */
  id: string;
  /** Render the short trailing form (".N" for nested nodes). */
  short?: boolean;
  /** Class applied to the id text. */
  className?: string;
  /** Style applied to the id text. */
  style?: CSSProperties;
  /** Optional test id placed on the wrapper. */
  testId?: string;
}

export function NodeID({ id, short = false, className, style, testId }: NodeIDProps) {
  const project = useProjectOptional();
  const showBadge = project?.isAll ?? false;
  const prefix = projectFromId(id);
  const text = short ? shortId(id) : id;

  return (
    <span
      className="inline-flex items-center gap-1"
      data-testid={testId}
      data-node-id={id}
    >
      {showBadge && prefix && (
        <span
          className="text-[9px] font-semibold uppercase tracking-wide px-1 py-px rounded leading-none"
          style={{
            backgroundColor: "var(--color-accent-muted)",
            color: "var(--color-accent)",
          }}
          data-testid="node-id-badge"
          title={`Project ${prefix}`}
        >
          {prefix}
        </span>
      )}
      <span className={className} style={style}>
        {text}
      </span>
    </span>
  );
}
