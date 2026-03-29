import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { SafeMarkdown, sanitizeHTML, markdownToHTML } from "../SafeMarkdown";

/**
 * SafeMarkdown tests per MTIX-9.5.2.
 * Tests markdown rendering and XSS sanitization.
 */

describe("SafeMarkdown", () => {
  it("renders headings", () => {
    render(<SafeMarkdown content="# Hello World" />);

    const md = screen.getByTestId("safe-markdown");
    expect(md.innerHTML).toContain("<h1>");
    expect(md.innerHTML).toContain("Hello World");
  });

  it("renders code blocks", () => {
    const content = "```go\nfunc main() {}\n```";
    render(<SafeMarkdown content={content} />);

    const md = screen.getByTestId("safe-markdown");
    expect(md.innerHTML).toContain("<pre><code");
    expect(md.innerHTML).toContain("func main()");
  });

  it("renders lists", () => {
    render(<SafeMarkdown content="- Item 1\n- Item 2" />);

    const md = screen.getByTestId("safe-markdown");
    expect(md.innerHTML).toContain("<li>");
    expect(md.innerHTML).toContain("Item 1");
    expect(md.innerHTML).toContain("Item 2");
  });

  it("renders links with target=_blank and rel=noopener", () => {
    render(
      <SafeMarkdown content="[Click here](https://example.com)" />,
    );

    const md = screen.getByTestId("safe-markdown");
    expect(md.innerHTML).toContain('target="_blank"');
    expect(md.innerHTML).toContain('rel="noopener noreferrer"');
    expect(md.innerHTML).toContain("https://example.com");
  });

  it("sanitizes <script> tags", () => {
    const result = sanitizeHTML("<script>alert('xss')</script><p>Safe</p>");
    expect(result).not.toContain("<script>");
    expect(result).not.toContain("alert");
    expect(result).toContain("<p>Safe</p>");
  });

  it("sanitizes onerror event handlers", () => {
    const result = sanitizeHTML('<img src="x" onerror="alert(1)">');
    expect(result).not.toContain("onerror");
    expect(result).not.toContain("alert");
  });

  it("sanitizes javascript: URLs", () => {
    const result = sanitizeHTML(
      '<a href="javascript:alert(1)">Click</a>',
    );
    expect(result).not.toContain("javascript:");
  });

  it("sanitizes data: URLs", () => {
    const result = sanitizeHTML(
      '<a href="data:text/html,<script>alert(1)</script>">Click</a>',
    );
    expect(result).not.toContain("data:");
  });

  it("allows https:// images", () => {
    const md = "![Alt](https://example.com/image.png)";
    const html = markdownToHTML(md);
    expect(html).toContain("<img");
    expect(html).toContain("https://example.com/image.png");
  });

  it("blocks non-https images", () => {
    const md = "![Alt](http://example.com/image.png)";
    const html = markdownToHTML(md);
    expect(html).not.toContain("<img");
  });

  it("renders bold and italic", () => {
    const md = "**bold** and *italic*";
    const html = markdownToHTML(md);
    expect(html).toContain("<strong>bold</strong>");
    expect(html).toContain("<em>italic</em>");
  });

  it("renders blockquotes", () => {
    const md = "> Important quote";
    const html = markdownToHTML(md);
    expect(html).toContain("<blockquote>");
  });

  it("renders horizontal rules", () => {
    const md = "---";
    const html = markdownToHTML(md);
    expect(html).toContain("<hr>");
  });

  it("strips iframe tags", () => {
    const result = sanitizeHTML(
      '<iframe src="https://evil.com"></iframe><p>Safe</p>',
    );
    expect(result).not.toContain("<iframe");
    expect(result).toContain("<p>Safe</p>");
  });

  it("strips onclick handlers", () => {
    const result = sanitizeHTML(
      '<button onclick="alert(1)">Click</button>',
    );
    expect(result).not.toContain("onclick");
  });
});
