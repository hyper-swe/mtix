import { describe, it, expect } from "vitest";

/**
 * Structural guard for the web UI's HTML and URL sinks (NFR-5.1): the only
 * place allowed to turn a string into live markup is SafeMarkdown (through the
 * sanitizing markdownToHTML), SafeMarkdown may be used only by audited
 * consumers, and no element URL (href, src and similar) is built from a
 * runtime value. A new sink or consumer must be reviewed, then listed here.
 */

const sources = import.meta.glob("/src/**/*.{ts,tsx,js,jsx,mjs,cjs}", {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

const production = Object.entries(sources)
  .map(([path, text]) => [path.replace(/^\/src\//, ""), text] as const)
  .filter(([path]) => !path.includes("__tests__") && !path.includes(".test.") && !path.startsWith("test/"));

const SANITIZER = "components/safeMarkdown.utils.ts";

function filesMatching(pattern: RegExp): string[] {
  return production.filter(([, text]) => pattern.test(text)).map(([path]) => path).sort();
}

describe("web UI HTML sinks", () => {
  it("scans the production source tree", () => {
    expect(production.length).toBeGreaterThan(20);
    expect(production.map(([p]) => p)).toContain("components/SafeMarkdown.tsx");
  });

  it("allows dangerouslySetInnerHTML only in SafeMarkdown", () => {
    expect(filesMatching(/dangerouslySetInnerHTML/)).toEqual(["components/SafeMarkdown.tsx"]);
  });

  it("allows other markup-injection APIs only in the sanitizer module", () => {
    const sinks = new RegExp(
      [
        "\\b(?:inner|outer)HTML\\b", // property reads, writes, += and bracketed access
        "insertAdjacentHTML",
        "document\\s*\\.\\s*write",
        "createContextualFragment",
        "srcdoc", // matched case-insensitively: srcDoc, srcdoc
        "setHTMLUnsafe",
        "parseHTMLUnsafe",
        "\\.setHTML\\b",
        "\\beval\\s*\\(",
        "\\bnew\\s+Function\\s*\\(",
        "\\bFunction\\s*\\(",
      ].join("|"),
      "i",
    );
    expect(filesMatching(sinks)).toEqual([SANITIZER]);
  });

  it("allows setAttribute only in the sanitizer module", () => {
    expect(filesMatching(/\bsetAttribute(?:NS)?\s*\(/)).toEqual([SANITIZER]);
  });

  it("builds no element URL from a runtime value", () => {
    // JSX expression values for URL-bearing attributes, for example href={x}
    // or src={`${x}`}. Audited list: empty, no such attribute exists today.
    const jsxUrl = /\s(?:href|src|srcSet|action|formAction|xlinkHref|xlink:href|poster)=\{/;
    expect(filesMatching(jsxUrl)).toEqual([]);
    // Spread props can smuggle the same attributes in.
    expect(filesMatching(/<(?:a|img|iframe|form|object|embed|area|base|link|source|video|audio|button|input)\b[^>]*\{\s*\.\.\./)).toEqual([]);
    // DOM property writes and navigation APIs.
    const domUrl = /\.(?:href|src|action|formAction|srcset|poster)\s*=[^=]|\bwindow\s*\.\s*open\s*\(|\bopen\s*\(\s*[^)]*_blank|\b(?:location|document\.location)\s*(?:\.\s*(?:assign|replace)\s*\(|=[^=])/;
    expect(filesMatching(domUrl)).toEqual([]);
  });

  it("builds no element by hand outside the sanitizer module", () => {
    expect(filesMatching(/\bcreateElement(?:NS)?\s*\(/)).toEqual([SANITIZER]);
  });

  it("allows only audited consumers of SafeMarkdown", () => {
    // Any string naming the module: static import, re-export, import() or require().
    const consumers = filesMatching(/["'][^"']*\/SafeMarkdown(?:\.tsx)?["']/i);
    expect(consumers).toEqual(["components/ActivityEntryRow.tsx"]);
  });

  it("allows only SafeMarkdown to import the markdown engine", () => {
    const users = filesMatching(/["'][^"']*safeMarkdown\.utils(?:\.ts)?["']/i);
    expect(users).toEqual(["components/SafeMarkdown.tsx"]);
  });

  it("allows no glob imports that could pull in a sink module", () => {
    expect(filesMatching(/import\.meta\.glob/)).toEqual([]);
  });
});
