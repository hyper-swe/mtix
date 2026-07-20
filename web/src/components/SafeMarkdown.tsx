/**
 * SafeMarkdown — XSS-safe markdown rendering per NFR-5.1.
 * Renders markdown to HTML with strict sanitization.
 * Strips: script, event handlers, javascript: URLs, data: URLs.
 * Allows: headings, paragraphs, lists, code blocks, links, bold, italic,
 * blockquote, hr, images (only https:// URLs).
 * Per MTIX-9.5.2.
 */

import { useMemo } from "react";
import { markdownToHTML } from "./safeMarkdown.utils";

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
