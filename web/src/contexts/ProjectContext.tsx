/**
 * Project context — manages the active project scope per FR-MULTI-PROJECT
 * (MP-14). One mtix DB can hold multiple projects (prefixes such as MTIX and
 * MTIX-DEV-OPS). A "primary" project is the default scope; the user can scope
 * to a single project or view all projects at once.
 *
 * Mirrors NavigationContext in shape. On mount it loads the project list from
 * GET /projects and defaults the active scope to the primary project (or a
 * previously persisted choice). The scope is persisted to localStorage so it
 * survives reloads.
 */

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { getProjects, type Project } from "../api/nodes";

/** Sentinel scope spanning every project. */
export const ALL_PROJECTS = "all";

/** localStorage key for the persisted active scope. */
const ACTIVE_PROJECT_KEY = "mtix-active-project";

interface ProjectContextValue {
  /** Distinct projects in the DB (prefix, count, isPrimary). */
  projects: Project[];
  /** Active scope: a project prefix or "all". */
  activeScope: string;
  /** The primary project prefix (default scope), or null until loaded. */
  primary: string | null;
  /** True while the project list is loading. */
  loading: boolean;
  /** Whether the active scope spans all projects. */
  isAll: boolean;
  /** Whether the DB holds more than one project. */
  isMultiProject: boolean;
  /** Change the active scope (a prefix or "all"). Persists to localStorage. */
  setActiveScope: (scope: string) => void;
  /**
   * The project a newly-created root should default into for the current
   * scope: the active project, or the primary when scope is "all".
   */
  defaultCreateProject: string | null;
}

const ProjectContext = createContext<ProjectContextValue | null>(null);

/** Read the persisted active scope, if any. */
function loadPersistedScope(): string | null {
  try {
    return window.localStorage.getItem(ACTIVE_PROJECT_KEY);
  } catch {
    return null;
  }
}

export function ProjectProvider({ children }: { children: ReactNode }) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [activeScope, setActiveScopeState] = useState<string>(
    () => loadPersistedScope() ?? ALL_PROJECTS,
  );
  const [loading, setLoading] = useState(true);

  const primary = useMemo(
    () => projects.find((p) => p.isPrimary)?.prefix ?? null,
    [projects],
  );

  // Load the project list once on mount and seed the default scope.
  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getProjects()
      .then((list) => {
        if (cancelled) return;
        setProjects(list);

        const persisted = loadPersistedScope();
        const prefixes = new Set(list.map((p) => p.prefix));
        const primaryPrefix = list.find((p) => p.isPrimary)?.prefix ?? null;

        // Honor a persisted scope only if it still resolves; otherwise default
        // to the primary project per MP-14.
        if (
          persisted &&
          (persisted === ALL_PROJECTS || prefixes.has(persisted))
        ) {
          setActiveScopeState(persisted);
        } else if (primaryPrefix) {
          setActiveScopeState(primaryPrefix);
        } else {
          setActiveScopeState(ALL_PROJECTS);
        }
      })
      .catch(() => {
        if (!cancelled) setProjects([]);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const setActiveScope = useCallback((scope: string) => {
    setActiveScopeState(scope);
    try {
      window.localStorage.setItem(ACTIVE_PROJECT_KEY, scope);
    } catch {
      // Ignore storage errors.
    }
  }, []);

  const isAll = activeScope === ALL_PROJECTS;
  const isMultiProject = projects.length > 1;
  const defaultCreateProject = isAll ? primary : activeScope || primary;

  return (
    <ProjectContext.Provider
      value={{
        projects,
        activeScope,
        primary,
        loading,
        isAll,
        isMultiProject,
        setActiveScope,
        defaultCreateProject,
      }}
    >
      {children}
    </ProjectContext.Provider>
  );
}

/** Access the project context. Throws if used outside ProjectProvider. */
export function useProject(): ProjectContextValue {
  const ctx = useContext(ProjectContext);
  if (!ctx) {
    throw new Error("useProject must be used within a ProjectProvider");
  }
  return ctx;
}

/**
 * Access the project context without requiring a provider. Returns null when
 * rendered outside a ProjectProvider, so shared widgets (e.g. <NodeID>) degrade
 * gracefully to single-project behavior in isolation/tests.
 */
export function useProjectOptional(): ProjectContextValue | null {
  return useContext(ProjectContext);
}
