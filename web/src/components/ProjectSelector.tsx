/**
 * Project scope selector per FR-MULTI-PROJECT (MP-15).
 *
 * Wires the TopBar scope control to ProjectContext: a dropdown listing the
 * primary project (default), each other project, and "All projects". When the
 * DB holds a single project the control is unobtrusive — it shows the project
 * name as plain text with no dropdown affordance. While the project list is
 * still loading, nothing is rendered.
 */

import { useEffect, useRef, useState } from "react";
import { ALL_PROJECTS, useProject } from "../contexts/ProjectContext";

/** Human label for the active scope. */
function scopeLabel(scope: string): string {
  return scope === ALL_PROJECTS ? "All projects" : scope;
}

export function ProjectSelector() {
  const { projects, activeScope, primary, loading, isMultiProject, setActiveScope } =
    useProject();
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  // Close the menu on outside click.
  useEffect(() => {
    if (!open) return;
    const handler = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [open]);

  // Nothing meaningful to show until the project list resolves.
  if (loading && projects.length === 0) {
    return <div className="mr-auto" data-testid="project-selector-loading" />;
  }

  // Single-project DB: unobtrusive — just the name, no control (MP-15).
  if (!isMultiProject) {
    const only = projects[0]?.prefix ?? primary;
    if (!only) {
      return <div className="mr-auto" data-testid="project-selector-empty" />;
    }
    return (
      <span
        className="text-xs px-2 py-1 mr-auto select-none"
        style={{ color: "var(--color-text-secondary)" }}
        data-testid="project-selector-single"
        title="Project"
      >
        {only}
      </span>
    );
  }

  // Build the ordered option list: primary first, then the rest, then "all".
  const others = projects
    .filter((p) => p.prefix !== primary)
    .map((p) => p.prefix)
    .sort();
  const options: string[] = [
    ...(primary ? [primary] : []),
    ...others,
    ALL_PROJECTS,
  ];

  return (
    <div ref={containerRef} className="relative mr-auto" data-testid="project-selector">
      <button
        className="flex items-center gap-1 text-xs px-2 py-1 rounded cursor-pointer"
        style={{
          color: "var(--color-text-secondary)",
          border: "1px solid var(--color-border)",
          borderRadius: "var(--radius-md)",
        }}
        onMouseEnter={(e) => (e.currentTarget.style.backgroundColor = "var(--color-hover)")}
        onMouseLeave={(e) => (e.currentTarget.style.backgroundColor = "transparent")}
        onClick={() => setOpen((prev) => !prev)}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label="Select project scope"
        data-testid="project-selector-button"
      >
        <span>{scopeLabel(activeScope)}</span>
        <Caret open={open} />
      </button>

      {open && (
        <div
          className="absolute left-0 mt-1 min-w-[180px] z-50 py-1 rounded"
          style={{
            backgroundColor: "var(--color-surface-overlay)",
            border: "1px solid var(--color-border)",
            boxShadow: "var(--shadow-md)",
            borderRadius: "var(--radius-md)",
          }}
          role="listbox"
          aria-label="Project scope"
          data-testid="project-selector-menu"
        >
          {options.map((opt) => {
            const isActive = opt === activeScope;
            const isAllOpt = opt === ALL_PROJECTS;
            const proj = projects.find((p) => p.prefix === opt);
            return (
              <button
                key={opt}
                role="option"
                aria-selected={isActive}
                className="w-full flex items-center justify-between gap-3 px-3 py-1.5 text-xs text-left cursor-pointer"
                style={{
                  color: isActive ? "var(--color-text-primary)" : "var(--color-text-secondary)",
                  backgroundColor: isActive ? "var(--color-accent-muted)" : "transparent",
                }}
                onMouseEnter={(e) => {
                  if (!isActive) e.currentTarget.style.backgroundColor = "var(--color-hover)";
                }}
                onMouseLeave={(e) => {
                  if (!isActive) e.currentTarget.style.backgroundColor = "transparent";
                }}
                onClick={() => {
                  setActiveScope(opt);
                  setOpen(false);
                }}
                data-testid={`project-option-${opt}`}
              >
                <span className="flex items-center gap-1.5">
                  {scopeLabel(opt)}
                  {opt === primary && (
                    <span
                      className="text-[9px] uppercase tracking-wide"
                      style={{ color: "var(--color-text-tertiary)" }}
                    >
                      primary
                    </span>
                  )}
                </span>
                {!isAllOpt && proj && (
                  <span
                    className="text-[10px] tabular-nums"
                    style={{ color: "var(--color-text-tertiary)" }}
                  >
                    {proj.count}
                  </span>
                )}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

function Caret({ open }: { open: boolean }) {
  return (
    <svg
      width="8"
      height="8"
      viewBox="0 0 10 10"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      style={{
        transform: open ? "rotate(180deg)" : "rotate(0deg)",
        transition: "transform var(--transition-fast)",
      }}
    >
      <path d="M2 3.5L5 6.5L8 3.5" />
    </svg>
  );
}
