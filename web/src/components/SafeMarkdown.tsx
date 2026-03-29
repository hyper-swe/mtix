/**
 * SafeMarkdown — XSS-safe markdown rendering per NFR-5.1.
 * Renders markdown to HTML with strict sanitization.
 * Strips: script, event handlers, javascript: URLs, data: URLs.
 * Allows: headings, paragraphs, lists, code blocks, links, bold, italic,
 * blockquote, hr, images (only https:// URLs).
 * Per MTIX-9.5.2.
 */

import { useMemo } from "react";

/**
 * Sanitize HTML string by removing dangerous elements and attributes.
 * This is the core XSS protection layer per NFR-5.1.
 */
function sanitizeHTML(html: string): string {
  // Strip <script> tags and content.
  let clean = html.replace(/<script\b[^<]*(?:(?!<\/script>)<[^<]*)*<\/script>/gi, "");

  // Strip event handlers (on*="...").
  clean = clean.replace(/\s+on\w+\s*=\s*["'][^"']*["']/gi, "");
  clean = clean.replace(/\s+on\w+\s*=\s*[^\s>]+/gi, "");

  // Strip javascript: URLs.
  clean = clean.replace(/href\s*=\s*["']\s*javascript:[^"']*["']/gi, 'href="#"');
  clean = clean.replace(/src\s*=\s*["']\s*javascript:[^"']*["']/gi, 'src=""');

  // Strip data: URLs.
  clean = clean.replace(/href\s*=\s*["']\s*data:[^"']*["']/gi, 'href="#"');
  clean = clean.replace(/src\s*=\s*["']\s*data:[^"']*["']/gi, 'src=""');

  // Strip dangerous elements: iframe, object, embed, form, style.
  clean = clean.replace(/<(iframe|object|embed|form|style|base|meta|link)\b[^>]*>[\s\S]*?<\/\1>/gi, "");
  clean = clean.replace(/<(iframe|object|embed|form|style|base|meta|link)\b[^>]*\/?>/gi, "");

  // Validate image src — only allow https:// URLs.
  clean = clean.replace(
    /<img\s+([^>]*)src\s*=\s*["'](?!https:\/\/)[^"']*["']([^>]*)>/gi,
    "",
  );

  return clean;
}

/**
 * Convert markdown text to sanitized HTML.
 * Lightweight parser supporting common markdown elements.
 */
function markdownToHTML(md: string): string {
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
  html = html.replace(/^[\-\*]\s+(.+)$/gm, "<li>$1</li>");
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

export interface SafeMarkdownProps {
  /** Markdown text to render. */
  content: string;
  /** Additional CSS class. */
  className?: string;
}

export function SafeMarkdown({ content, className = "" }: SafeMarkdownProps) {
  const html = useMemo(() => markdownToHTML(content), [content]);

  return (
    <div
      className={`prose prose-sm max-w-none ${className}`}
      data-testid="safe-markdown"
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

// Export for testing.
export { sanitizeHTML, markdownToHTML };
