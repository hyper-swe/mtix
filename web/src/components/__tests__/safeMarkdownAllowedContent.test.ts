import { describe, it, expect } from "vitest";
import { markdownToHTML, sanitizeHTML } from "../safeMarkdown.utils";

// Template content stays inert: these checks neither execute code nor load resources.
function fragment(html: string): DocumentFragment {
  const template = document.createElement("template");
  template.innerHTML = html;
  return template.content;
}

const markdown = '![image](https://example.invalid/image" /onerror="window.marker=1")';

describe("allowed Markdown content", () => {
  it("keeps only permitted attributes after Markdown conversion", () => {
    const content = fragment(markdownToHTML(markdown));
    expect(content.querySelector("img")).not.toBeNull();
    expect(content.querySelector("[onerror]")).toBeNull();
  });

  it.each([
    ["attributes", '<p style="color:red" onclick="window.marker=1" data-extra="value">safe</p>'],
    ["elements", '<video><source></video><button>button</button><math><mtext>math</mtext></math>'],
    ["template", '<template><img src="https://example.invalid/a" /onerror="window.marker=1"></template>'],
    ["foreign content", '<svg><a href="https://example.invalid/">foreign</a></svg>'],
  ])("limits %s to the allowlist", (_name, html) => {
    const content = fragment(sanitizeHTML(html));
    const allowed = new Set(["A", "BLOCKQUOTE", "BR", "CODE", "EM", "H1", "H2", "H3", "H4", "H5", "H6", "HR", "IMG", "LI", "OL", "P", "PRE", "STRONG", "UL"]);
    for (const element of content.querySelectorAll("*")) {
      expect(allowed.has(element.tagName)).toBe(true);
      for (const attribute of element.attributes) {
        expect(attribute.name.startsWith("on")).toBe(false);
        expect(["style", "data-extra"]).not.toContain(attribute.name);
      }
    }
    expect(content.querySelector("template,svg,math,video,button")).toBeNull();
  });

  it.each(["https://example.invalid/", "http://example.invalid/", "mailto:reader@example.invalid"])("preserves a permitted link %s", (href) => {
    const link = fragment(sanitizeHTML(`<a href="${href}" target="_self" rel="opener" style="color:red">safe</a>`)).querySelector("a");
    expect(link?.getAttribute("href")).toBe(href);
    expect(link?.getAttribute("target")).toBe("_blank");
    expect(link?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(link?.hasAttribute("style")).toBe(false);
  });

  it.each(["javascript:window.marker=1", "java\tscript:window.marker=1", "data:text/html,noop", "file:///unused", "ftp://example.invalid/", "//example.invalid/", "relative/path", "http://[", ""])("omits an unpermitted link %s", (href) => {
    const link = fragment(sanitizeHTML(`<a href="${href}">safe</a>`)).querySelector("a");
    expect(link?.hasAttribute("href")).toBe(false);
  });

  it.each(["http://example.invalid/a", "data:image/png,noop", "javascript:window.marker=1", ""])("keeps the https-only image rule %s", (src) => {
    expect(fragment(sanitizeHTML(`<img src="${src}" alt="safe">`)).querySelector("img")).toBeNull();
  });

  it("preserves permitted image and code attributes and is idempotent", () => {
    const html = '<p title="unused">safe</p><img src="https://example.invalid/a" alt="safe" style="color:red"><pre><code class="language-go" id="unused">body</code></pre>';
    const sanitized = sanitizeHTML(html), content = fragment(sanitized);
    expect(content.querySelector("img")?.getAttribute("alt")).toBe("safe");
    expect(content.querySelector("img")?.getAttribute("loading")).toBe("lazy");
    expect(content.querySelector("code")?.getAttribute("class")).toBe("language-go");
    expect(content.querySelector("[style],[id],[title]")).toBeNull();
    expect(sanitizeHTML(sanitized)).toBe(sanitized);
    expect(fragment(sanitizeHTML('<code class="untrusted">body</code>')).querySelector("code")?.hasAttribute("class")).toBe(false);
    expect(fragment(sanitizeHTML('<img alt="missing">')).querySelector("img")).toBeNull();
  });
});
