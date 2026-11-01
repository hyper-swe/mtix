import { expect } from "vitest";
import type { Node } from "../../types";

/**
 * Shared hostile payload and assertions for the content-inertness regressions
 * (NFR-5.1). The payload combines script, image, frame, link, handler and
 * markdown forms, including javascript: URLs.
 */
export const HOSTILE =
  '<img src=x onerror="window.__inert=1"><script>window.__inert=2</script>' +
  '<iframe src="javascript:window.__inert=3"></iframe> <a href="javascript:1" onclick="window.__inert=4">x</a> **b** [l](javascript:1)';

/** Short payload for surfaces that truncate long titles (the dependency graph). */
export const HOSTILE_SHORT = '<b onclick="1">x</b>';

const URL_ATTRS = ["href", "src", "srcdoc", "action", "formaction", "xlink:href", "poster", "data"];

/**
 * Assert the rendered subtree holds no live element or URL built from content:
 * no script, frame, image, link, form or embed element, no URL-bearing
 * attribute carrying a script URL, and no inline event handler. Under jsdom
 * scripts do not run, so the structural check is the signal.
 */
export function expectNoLiveContent(root: HTMLElement): void {
  expect(root.querySelector("script, iframe, img, a, form, object, embed, link, base")).toBeNull();
  for (const el of Array.from(root.querySelectorAll("*"))) {
    for (const attr of Array.from(el.attributes)) {
      expect(attr.name.startsWith("on")).toBe(false);
      if (URL_ATTRS.includes(attr.name.toLowerCase())) {
        expect(attr.value).not.toMatch(/javascript:|__inert|<script|onerror/i);
      }
    }
  }
}

/** Count text nodes under root that contain the whole payload verbatim. */
export function literalCount(root: HTMLElement, payload: string): number {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  let n = 0;
  for (let t = walker.nextNode(); t; t = walker.nextNode()) {
    if ((t.textContent ?? "").includes(payload)) n++;
  }
  return n;
}

/**
 * Assert the payload is shown as literal text at least min times and that no
 * live content exists anywhere in the subtree.
 */
export function expectInertText(root: HTMLElement, payload: string = HOSTILE, min = 1): void {
  expect(literalCount(root, payload)).toBeGreaterThanOrEqual(min);
  expectNoLiveContent(root);
}

/** Build a node with every field present. */
export function makeNode(overrides: Partial<Node> = {}): Node {
  return {
    id: "PROJ-1.1.2", parent_id: "PROJ-1.1", project: "PROJ", depth: 2, seq: 2,
    title: "Title", description: "Description", prompt: "Prompt", acceptance: "Acceptance",
    labels: [], priority: 2 as const, status: "in_progress", node_type: "issue",
    issue_type: "bug", creator: "vimal", assignee: "agent-claude", agent_state: "working",
    weight: 1, progress: 0.5, content_hash: "", child_count: 0,
    created_at: "2026-03-08T10:00:00Z", updated_at: "2026-03-08T12:00:00Z",
    closed_at: null, defer_until: null, deleted_at: null, annotations: [],
    ...overrides,
  };
}
