import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";

/** Theme mode: light, dark, or follow system. */
export type ThemeMode = "light" | "dark" | "system";

/** Density mode per FR-UI-15. */
export type Density = "comfortable" | "compact";

interface ThemeContextValue {
  /** The resolved theme (always light or dark, never system). */
  theme: "light" | "dark";
  /** The user's preference (may be system). */
  themeMode: ThemeMode;
  /** Row density. */
  density: Density;
  /** Set theme mode. */
  setThemeMode: (mode: ThemeMode) => void;
  /** Toggle between light and dark. */
  toggleTheme: () => void;
  /** Set density. */
  setDensity: (d: Density) => void;
  /** Toggle between comfortable and compact. */
  toggleDensity: () => void;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

const THEME_STORAGE_KEY = "mtix-theme";
const DENSITY_STORAGE_KEY = "mtix-density";

/**
 * Resolve theme mode to concrete light/dark value.
 * Detects system preference via prefers-color-scheme media query.
 */
function resolveTheme(mode: ThemeMode): "light" | "dark" {
  if (mode === "system") {
    if (typeof window !== "undefined") {
      return window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light";
    }
    return "light";
  }
  return mode;
}

/** Load persisted theme mode, defaulting to system. */
function loadThemeMode(): ThemeMode {
  if (typeof window === "undefined") return "system";
  const stored = window.localStorage.getItem(THEME_STORAGE_KEY);
  if (stored === "light" || stored === "dark" || stored === "system") {
    return stored;
  }
  return "system";
}

/** Load persisted density, defaulting to comfortable. */
function loadDensity(): Density {
  if (typeof window === "undefined") return "comfortable";
  const stored = window.localStorage.getItem(DENSITY_STORAGE_KEY);
  if (stored === "comfortable" || stored === "compact") {
    return stored;
  }
  return "comfortable";
}

/**
 * ThemeProvider manages light/dark theme and density.
 * Per FR-UI-16: detects system preference, supports manual toggle,
 * persists choice across sessions.
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [themeMode, setThemeModeState] = useState<ThemeMode>(loadThemeMode);
  const [density, setDensityState] = useState<Density>(loadDensity);
  const [theme, setTheme] = useState<"light" | "dark">(() =>
    resolveTheme(loadThemeMode()),
  );

  // Apply theme class to document element.
  useEffect(() => {
    const resolved = resolveTheme(themeMode);
    setTheme(resolved);

    const root = document.documentElement;
    root.classList.toggle("dark", resolved === "dark");
  }, [themeMode]);

  // Apply density class to document element.
  useEffect(() => {
    const root = document.documentElement;
    root.classList.toggle("density-compact", density === "compact");
  }, [density]);

  // Listen for system theme changes when in system mode.
  useEffect(() => {
    if (themeMode !== "system") return;

    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const handler = (e: MediaQueryListEvent) => {
      setTheme(e.matches ? "dark" : "light");
      document.documentElement.classList.toggle("dark", e.matches);
    };
    mq.addEventListener("change", handler);
    return () => mq.removeEventListener("change", handler);
  }, [themeMode]);

  // Listen for Cmd+D to toggle density per FR-UI-15.
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "d") {
        e.preventDefault();
        setDensityState((prev) => {
          const next = prev === "comfortable" ? "compact" : "comfortable";
          window.localStorage.setItem(DENSITY_STORAGE_KEY, next);
          return next;
        });
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []);

  const setThemeMode = useCallback((mode: ThemeMode) => {
    setThemeModeState(mode);
    window.localStorage.setItem(THEME_STORAGE_KEY, mode);
  }, []);

  const toggleTheme = useCallback(() => {
    setThemeModeState((prev) => {
      const next = resolveTheme(prev) === "dark" ? "light" : "dark";
      window.localStorage.setItem(THEME_STORAGE_KEY, next);
      return next;
    });
  }, []);

  const setDensity = useCallback((d: Density) => {
    setDensityState(d);
    window.localStorage.setItem(DENSITY_STORAGE_KEY, d);
  }, []);

  const toggleDensity = useCallback(() => {
    setDensityState((prev) => {
      const next = prev === "comfortable" ? "compact" : "comfortable";
      window.localStorage.setItem(DENSITY_STORAGE_KEY, next);
      return next;
    });
  }, []);

  return (
    <ThemeContext.Provider
      value={{
        theme,
        themeMode,
        density,
        setThemeMode,
        toggleTheme,
        setDensity,
        toggleDensity,
      }}
    >
      {children}
    </ThemeContext.Provider>
  );
}

/** Hook to access theme context. Throws if used outside ThemeProvider. */
export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) {
    throw new Error("useTheme must be used within a ThemeProvider");
  }
  return ctx;
}
