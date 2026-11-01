/** Every shared hostile fixture rendered through SafeMarkdown stays inert (NFR-5.1). */
import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { SafeMarkdown } from "../SafeMarkdown";
import { ActivityEntryRow } from "../ActivityEntryRow";
import { hostileFixtures } from "../../test/hostileFixtures";

const URL_ATTRS = ["href", "src", "srcdoc", "action", "formaction", "xlink:href"];

function expectInert(root: HTMLElement): void {
  expect(root.querySelector("script, iframe, form, object, embed, link, base, style")).toBeNull();
  for (const el of Array.from(root.querySelectorAll("*"))) {
    for (const attr of Array.from(el.attributes)) {
      expect(attr.name.toLowerCase().startsWith("on")).toBe(false);
      if (URL_ATTRS.includes(attr.name.toLowerCase())) {
        expect(attr.value.trim()).not.toMatch(/^\s*(javascript|data|vbscript):/i);
      }
    }
  }
}

describe("SafeMarkdownHostile", () => {
  it.each([...hostileFixtures.ids, ...hostileFixtures.text])("SafeMarkdown renders $Name inertly", ({ Value }) => {
    const { container } = render(<SafeMarkdown content={Value} />);
    expectInert(container);
  });

  it.each([...hostileFixtures.ids, ...hostileFixtures.text])("ActivityEntryRow comment renders $Name inertly", ({ Value }) => {
    const { container } = render(<ActivityEntryRow entry={{
      id: "a1", node_id: "N-1", type: "comment", author: Value, text: Value,
      created_at: "2026-10-28T00:00:00Z",
    } as never} />);
    expectInert(container);
  });

  it("drops javascript: link targets from markdown", () => {
    const { container } = render(<SafeMarkdown content={'[x](javascript:alert(1)) <a href="javascript:1">y</a>'} />);
    expectInert(container);
    expect(container.textContent).toContain("x");
  });
});
