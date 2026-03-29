import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import { ThemeProvider, useTheme } from "../ThemeContext";

/**
 * Theme context tests per MTIX-9.1.4.
 * Tests system preference detection, manual toggle, persistence,
 * status colors, and density toggle.
 */

// Test component that exposes theme values.
function ThemeTestConsumer() {
  const { theme, themeMode, density, toggleTheme, toggleDensity, setThemeMode } =
    useTheme();
  return (
    <div>
      <span data-testid="theme">{theme}</span>
      <span data-testid="theme-mode">{themeMode}</span>
      <span data-testid="density">{density}</span>
      <button onClick={toggleTheme}>toggle-theme</button>
      <button onClick={toggleDensity}>toggle-density</button>
      <button onClick={() => setThemeMode("dark")}>set-dark</button>
      <button onClick={() => setThemeMode("light")}>set-light</button>
      <button onClick={() => setThemeMode("system")}>set-system</button>
    </div>
  );
}

beforeEach(() => {
  window.localStorage.clear();
  document.documentElement.classList.remove("dark", "density-compact");
});

describe("ThemeContext", () => {
  it("defaults to system preference — light when system is light", () => {
    // matchMedia mock returns false for dark in setup.ts.
    render(
      <ThemeProvider>
        <ThemeTestConsumer />
      </ThemeProvider>,
    );

    expect(screen.getByTestId("theme")).toHaveTextContent("light");
    expect(screen.getByTestId("theme-mode")).toHaveTextContent("system");
  });

  it("applies dark theme when system prefers dark", () => {
    // Override matchMedia to prefer dark.
    const originalMatchMedia = window.matchMedia;
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: (query: string) => ({
        matches: query === "(prefers-color-scheme: dark)",
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      }),
    });

    render(
      <ThemeProvider>
        <ThemeTestConsumer />
      </ThemeProvider>,
    );

    expect(screen.getByTestId("theme")).toHaveTextContent("dark");

    // Restore.
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: originalMatchMedia,
    });
  });

  it("toggles between light and dark", () => {
    render(
      <ThemeProvider>
        <ThemeTestConsumer />
      </ThemeProvider>,
    );

    expect(screen.getByTestId("theme")).toHaveTextContent("light");

    fireEvent.click(screen.getByText("toggle-theme"));
    expect(screen.getByTestId("theme")).toHaveTextContent("dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);

    fireEvent.click(screen.getByText("toggle-theme"));
    expect(screen.getByTestId("theme")).toHaveTextContent("light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("persists theme choice to localStorage", () => {
    render(
      <ThemeProvider>
        <ThemeTestConsumer />
      </ThemeProvider>,
    );

    fireEvent.click(screen.getByText("set-dark"));
    expect(window.localStorage.getItem("mtix-theme")).toBe("dark");

    fireEvent.click(screen.getByText("set-light"));
    expect(window.localStorage.getItem("mtix-theme")).toBe("light");
  });

  it("loads persisted theme on mount", () => {
    window.localStorage.setItem("mtix-theme", "dark");

    render(
      <ThemeProvider>
        <ThemeTestConsumer />
      </ThemeProvider>,
    );

    expect(screen.getByTestId("theme")).toHaveTextContent("dark");
    expect(screen.getByTestId("theme-mode")).toHaveTextContent("dark");
  });

  it("defaults to comfortable density", () => {
    render(
      <ThemeProvider>
        <ThemeTestConsumer />
      </ThemeProvider>,
    );

    expect(screen.getByTestId("density")).toHaveTextContent("comfortable");
  });

  it("toggles density between comfortable and compact", () => {
    render(
      <ThemeProvider>
        <ThemeTestConsumer />
      </ThemeProvider>,
    );

    fireEvent.click(screen.getByText("toggle-density"));
    expect(screen.getByTestId("density")).toHaveTextContent("compact");
    expect(
      document.documentElement.classList.contains("density-compact"),
    ).toBe(true);

    fireEvent.click(screen.getByText("toggle-density"));
    expect(screen.getByTestId("density")).toHaveTextContent("comfortable");
  });

  it("responds to Cmd+D keyboard shortcut for density toggle", () => {
    render(
      <ThemeProvider>
        <ThemeTestConsumer />
      </ThemeProvider>,
    );

    expect(screen.getByTestId("density")).toHaveTextContent("comfortable");

    act(() => {
      window.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "d",
          metaKey: true,
          bubbles: true,
        }),
      );
    });

    expect(screen.getByTestId("density")).toHaveTextContent("compact");
  });

  it("persists density choice to localStorage", () => {
    render(
      <ThemeProvider>
        <ThemeTestConsumer />
      </ThemeProvider>,
    );

    fireEvent.click(screen.getByText("toggle-density"));
    expect(window.localStorage.getItem("mtix-density")).toBe("compact");
  });

  it("throws when useTheme is used outside ThemeProvider", () => {
    // Suppress React error boundary console output.
    const consoleSpy = vi.spyOn(console, "error").mockImplementation(() => {});

    expect(() => {
      render(<ThemeTestConsumer />);
    }).toThrow("useTheme must be used within a ThemeProvider");

    consoleSpy.mockRestore();
  });
});

describe("Theme CSS variables", () => {
  it("sets CSS variables on document root for light theme", () => {
    render(
      <ThemeProvider>
        <ThemeTestConsumer />
      </ThemeProvider>,
    );

    // The CSS custom properties are set via index.css :root selector,
    // not by JS, so we verify the class toggles instead.
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("sets dark class on document root for dark theme", () => {
    render(
      <ThemeProvider>
        <ThemeTestConsumer />
      </ThemeProvider>,
    );

    fireEvent.click(screen.getByText("set-dark"));
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });
});
