/**
 * XSS-safe markdown → HTML engine for SafeMarkdown, per NFR-5.1 / MTIX-9.5.2.
 * Kept in its own module (no React) so it is independently testable and so the
 * component file only exports a component (react-refresh/only-export-components).
 * Strips: script, event handlers, javascript: URLs, data: URLs. Allows:
 * headings, paragraphs, lists, code blocks, links, bold, italic, blockquote,
 * hr, images (only https:// URLs).
 */

/** Validate only explicitly permitted absolute URL schemes (NFR-5.1). */
function permittedURL(value: string | null, protocols: readonly string[]): string | null {
  if (!value) return null;
  try {
    const url = new URL(value);
    return protocols.includes(url.protocol) ? value : null;
  } catch {
    return null;
  }
}

function permittedAttribute(tag: string, attribute: Attr): boolean {
  if (tag === "A") return attribute.name === "href";
  if (tag === "IMG") return attribute.name === "src" || attribute.name === "alt";
  return tag === "CODE" && attribute.name === "class" && /^language-\w+$/.test(attribute.value);
}

/** Add trusted link/image attributes only after validating their URLs. */
function sanitizeURLAttributes(element: Element, tag: string): void {
  if (tag === "A") {
    const href = permittedURL(element.getAttribute("href"), ["http:", "https:", "mailto:"]);
    if (href === null) {
      element.removeAttribute("href");
    } else {
      element.setAttribute("href", href);
      element.setAttribute("target", "_blank");
      element.setAttribute("rel", "noopener noreferrer");
    }
  } else if (tag === "IMG") {
    const src = permittedURL(element.getAttribute("src"), ["https:"]);
    if (src === null) {
      element.remove();
    } else {
      element.setAttribute("src", src);
      element.setAttribute("loading", "lazy");
    }
  }
}

/**
 * Sanitize parsed DOM with an element/attribute allowlist per NFR-5.1.
 * Parsed nodes remain in an inert template until the cleaned HTML is used.
 */
export function sanitizeHTML(html: string): string {
  const template = document.createElement("template");
  template.innerHTML = html;
  const allowed = new Set([
    "A", "BLOCKQUOTE", "BR", "CODE", "EM", "H1", "H2", "H3", "H4", "H5", "H6",
    "HR", "IMG", "LI", "OL", "P", "PRE", "STRONG", "UL",
  ]);
  for (const element of template.content.querySelectorAll("*")) {
    const tag = element.tagName.toUpperCase();
    if (!allowed.has(tag) || element.namespaceURI !== "http://www.w3.org/1999/xhtml") {
      element.remove();
      continue;
    }
    for (const attribute of Array.from(element.attributes)) {
      if (!permittedAttribute(tag, attribute)) element.removeAttribute(attribute.name);
    }
    sanitizeURLAttributes(element, tag);
  }
  return template.innerHTML;
}

/**
 * Convert markdown text to sanitized HTML.
 * Lightweight parser supporting common markdown elements.
 */
export function markdownToHTML(md: string): string {
  let html = md;

  // Escape raw HTML entities first (for text that isn't markdown syntax).
  // We'll re-introduce allowed HTML after parsing.
  html = html
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");

  // Code blocks (``` ... ```) — must come before other transforms.
  html = html.replace(
    /```(\w+)?\n([\s\S]*?)```/g,
    (_match, lang: string | undefined, code: string) => {
      const langClass = lang ? ` class="language-${lang}"` : "";
      return `<pre><code${langClass}>${code.trim()}</code></pre>`;
    },
  );

  // Inline code (`...`).
  html = html.replace(/`([^`]+)`/g, "<code>$1</code>");

  // Headings (# to ######).
  html = html.replace(/^######\s+(.+)$/gm, "<h6>$1</h6>");
  html = html.replace(/^#####\s+(.+)$/gm, "<h5>$1</h5>");
  html = html.replace(/^####\s+(.+)$/gm, "<h4>$1</h4>");
  html = html.replace(/^###\s+(.+)$/gm, "<h3>$1</h3>");
  html = html.replace(/^##\s+(.+)$/gm, "<h2>$1</h2>");
  html = html.replace(/^#\s+(.+)$/gm, "<h1>$1</h1>");

  // Horizontal rule.
  html = html.replace(/^---$/gm, "<hr>");
  html = html.replace(/^\*\*\*$/gm, "<hr>");

  // Blockquote.
  html = html.replace(/^&gt;\s+(.+)$/gm, "<blockquote>$1</blockquote>");

  // Bold (**text** or __text__).
  html = html.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
  html = html.replace(/__(.+?)__/g, "<strong>$1</strong>");

  // Italic (*text* or _text_).
  html = html.replace(/\*(.+?)\*/g, "<em>$1</em>");
  html = html.replace(/_(.+?)_/g, "<em>$1</em>");

  // Images ![alt](url) — only https:// allowed.
  html = html.replace(
    /!\[([^\]]*)\]\((https:\/\/[^)]+)\)/g,
    '<img src="$2" alt="$1" loading="lazy">',
  );
  // Remove non-https image references.
  html = html.replace(/!\[([^\]]*)\]\((?!https:\/\/)[^)]+\)/g, "[$1]");

  // Links [text](url) — target=_blank, rel=noopener.
  html = html.replace(
    /\[([^\]]+)\]\((https?:\/\/[^)]+)\)/g,
    '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>',
  );

  // Unordered lists (- item or * item).
  html = html.replace(/^[-*]\s+(.+)$/gm, "<li>$1</li>");
  html = html.replace(/(<li>[\s\S]*?<\/li>)/g, "<ul>$1</ul>");
  // Merge adjacent <ul> blocks.
  html = html.replace(/<\/ul>\s*<ul>/g, "");

  // Ordered lists (1. item).
  html = html.replace(/^\d+\.\s+(.+)$/gm, "<li>$1</li>");

  // Paragraphs — wrap remaining text lines.
  const lines = html.split("\n");
  const result: string[] = [];
  for (const line of lines) {
    const trimmed = line.trim();
    if (
      trimmed === "" ||
      trimmed.startsWith("<h") ||
      trimmed.startsWith("<pre") ||
      trimmed.startsWith("<ul") ||
      trimmed.startsWith("<ol") ||
      trimmed.startsWith("<li") ||
      trimmed.startsWith("<blockquote") ||
      trimmed.startsWith("<hr") ||
      trimmed.startsWith("</")
    ) {
      result.push(line);
    } else {
      result.push(`<p>${line}</p>`);
    }
  }
  html = result.join("\n");

  // Sanitize the final HTML.
  return sanitizeHTML(html);
}
