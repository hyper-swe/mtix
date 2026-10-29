/** Grep guard: user content may enter HTML only through audited SafeMarkdown. */
import { describe, expect, it } from "vitest";

const sources = import.meta.glob("/src/**/*.{ts,tsx,js,jsx,mjs,cjs}", {
  query: "?raw", import: "default", eager: true,
}) as Record<string, string>;

describe("noInnerHtml", () => {
  it("scans production sources and permits only the SafeMarkdown boundary", () => {
    const production = Object.entries(sources).filter(([path]) =>
      !path.includes("__tests__") && !path.includes(".test.") && !path.startsWith("/src/test/"));
    expect(production.length).toBeGreaterThan(20);
    const offenders = production.filter(([path, text]) =>
      !["/src/components/SafeMarkdown.tsx", "/src/components/safeMarkdown.utils.ts"].includes(path)
      && /\b(?:innerHTML|outerHTML|dangerouslySetInnerHTML|insertAdjacentHTML)\b/i.test(text));
    expect(offenders.map(([path]) => path)).toEqual([]);
  });
});
