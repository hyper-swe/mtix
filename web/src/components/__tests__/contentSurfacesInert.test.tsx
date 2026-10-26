import { describe, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { NodeDetail } from "../NodeDetail";
import { PromptEditor } from "../PromptEditor";
import { ContextChain } from "../ContextChain";
import { AgentDashboard } from "../AgentDashboard";
import { expectInertText, expectNoLiveContent, HOSTILE, makeNode } from "./contentInertHelpers";
import type { Node, ContextEntry, Dependency, AgentInfo } from "../../types";

/**
 * Hardening regressions: every plain-text content surface in the web UI must
 * render hostile node and annotation content as literal text (NFR-5.1).
 * Each test fails if its surface starts interpreting the content as markup or
 * builds an element or URL attribute from it. Scripts do not run under jsdom, so
 * the assertions are structural: literal text present, no live element, no
 * script URL or event handler anywhere in the rendered subtree.
 */

const detailProps = {
  children: [] as Node[],
  contextChain: [] as ContextEntry[],
  activityEntries: [],
  activityHasMore: false,
  onLoadMoreActivity: vi.fn(),
  dependencies: [] as Dependency[],
  onUpdateTitle: vi.fn(),
  onStatusChange: vi.fn(),
  onSavePrompt: vi.fn(),
  onSaveAndRerun: vi.fn(),
  onAddAnnotation: vi.fn(),
  onResolveAnnotation: vi.fn(),
  onNavigate: vi.fn(),
  onCreateChild: vi.fn(),
  onBulkAction: vi.fn(),
};

const editorProps = {
  prompt: "Original prompt",
  annotations: [],
  isOpen: true,
  onSave: vi.fn(),
  onSaveAndRerun: vi.fn(),
  onCancel: vi.fn(),
  onAddAnnotation: vi.fn(),
  onResolveAnnotation: vi.fn(),
};

describe("hostile content stays inert on plain-text surfaces", () => {
  it("node title in the detail header", () => {
    const { container } = render(<NodeDetail {...detailProps} node={makeNode({ title: HOSTILE })} />);
    expectInertText(screen.getByTestId("node-title"));
    expectNoLiveContent(container);
  });

  it("node description", () => {
    const { container } = render(<NodeDetail {...detailProps} node={makeNode({ description: HOSTILE })} />);
    expectInertText(screen.getByTestId("description-content"));
    expectNoLiveContent(container);
  });

  it("node prompt display", () => {
    const { container } = render(<NodeDetail {...detailProps} node={makeNode({ prompt: HOSTILE })} />);
    expectInertText(screen.getByTestId("prompt-display"));
    expectNoLiveContent(container);
  });

  it("assignee in the detail header", () => {
    const { container } = render(<NodeDetail {...detailProps} node={makeNode({ assignee: HOSTILE })} />);
    expectInertText(screen.getByTestId("assignee"));
    expectNoLiveContent(container);
  });

  it("hostile content in every field builds no element or URL attribute", () => {
    const node = makeNode({
      title: HOSTILE, description: HOSTILE, prompt: HOSTILE, assignee: HOSTILE, creator: HOSTILE,
      labels: [HOSTILE], acceptance: HOSTILE,
    });
    const { container } = render(<NodeDetail {...detailProps} node={node} />);
    expectInertText(container, HOSTILE, 4);
  });

  it("child node titles and assignees in the children list", () => {
    const child = makeNode({ id: "PROJ-1.1.2.1", title: HOSTILE, assignee: HOSTILE, depth: 3, status: "open" });
    const { container } = render(<NodeDetail {...detailProps} node={makeNode()} children={[child]} />);
    expectInertText(container, HOSTILE, 1);
  });

  it("prompt annotations, including the author", () => {
    const annotations = [
      { id: "a1", author: HOSTILE, text: HOSTILE, created_at: "2026-03-08T10:00:00Z", resolved: false },
    ];
    render(<PromptEditor {...editorProps} annotations={annotations} />);
    expectInertText(screen.getByTestId("annotation-item"), HOSTILE, 2);
  });

  it("prompt diff lines", () => {
    render(<PromptEditor {...editorProps} />);
    fireEvent.change(screen.getByTestId("prompt-textarea"), { target: { value: HOSTILE } });
    fireEvent.click(screen.getByTestId("diff-toggle"));
    expectInertText(screen.getByTestId("diff-view"));
  });

  it("context chain titles and expanded prompts", () => {
    const chain: ContextEntry[] = [
      { id: "PROJ-1", title: HOSTILE, status: "in_progress", prompt: HOSTILE, acceptance: "", depth: 0 },
      { id: "PROJ-1.1", title: "Current", status: "in_progress", prompt: "p", acceptance: "", depth: 1 },
    ];
    const { container } = render(<ContextChain chain={chain} currentNodeId="PROJ-1.1" onNavigate={vi.fn()} />);
    fireEvent.click(screen.getByLabelText("Toggle context chain"));
    expectInertText(screen.getByTestId("prompt-PROJ-1"));
    expectInertText(screen.getByTestId("chain-link-PROJ-1"));
    expectNoLiveContent(container);
  });

  it("agent dashboard action text and current node title", () => {
    const agent: AgentInfo = {
      agent_id: "agent-x", state: "working", current_node_id: "PROJ-1", current_node_title: HOSTILE,
      session_started_at: "2026-03-08T10:00:00Z", last_heartbeat: new Date().toISOString(),
      nodes_completed: 1,
      recent_actions: [{ id: "r1", type: "agent_done", author: "agent-x", text: HOSTILE, created_at: "2026-03-08T10:00:00Z" }],
    };
    const { container } = render(<AgentDashboard agents={[agent]} onNavigate={vi.fn()} />);
    expectInertText(screen.getByTestId("agent-action"));
    expectInertText(screen.getByTestId("current-work"));
    expectNoLiveContent(container);
  });
});
