/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{js,ts,jsx,tsx}"],
  darkMode: "class",
  theme: {
    extend: {
      colors: {
        // mtix design system colors per requirement-ui.md §9.
        surface: {
          light: "#FFFFFF",
          dark: "#222244",
        },
        background: {
          light: "#FAFAFA",
          dark: "#1A1A2E",
        },
        accent: {
          DEFAULT: "#6366F1",
          light: "#6366F1",
          dark: "#818CF8",
        },
        status: {
          done: { light: "#10B981", dark: "#34D399" },
          "in-progress": { light: "#3B82F6", dark: "#60A5FA" },
          blocked: { light: "#EF4444", dark: "#F87171" },
          open: { light: "#6B7280", dark: "#9CA3AF" },
          invalidated: { light: "#F59E0B", dark: "#FBBF24" },
          deferred: { light: "#8B5CF6", dark: "#A78BFA" },
          cancelled: { light: "#9CA3AF", dark: "#6B7280" },
        },
      },
      fontFamily: {
        sans: [
          "Inter",
          "system-ui",
          "-apple-system",
          "BlinkMacSystemFont",
          "Segoe UI",
          "Roboto",
          "sans-serif",
        ],
        mono: [
          "JetBrains Mono",
          "Fira Code",
          "ui-monospace",
          "SFMono-Regular",
          "monospace",
        ],
      },
      fontSize: {
        "node-title": ["14px", { lineHeight: "20px", fontWeight: "500" }],
        "node-prompt": ["13px", { lineHeight: "18px", fontWeight: "400" }],
        "node-id": ["12px", { lineHeight: "16px", fontWeight: "400" }],
        "status-badge": ["11px", { lineHeight: "16px", fontWeight: "700" }],
      },
    },
  },
  plugins: [],
};
