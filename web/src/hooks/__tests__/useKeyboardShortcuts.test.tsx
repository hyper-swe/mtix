import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, act } from "@testing-library/react";
import {
  useKeyboardShortcuts,
  type Shortcut,
} from "../useKeyboardShortcuts";

/**
 * Keyboard shortcuts tests per MTIX-9.2.2.
 * Tests navigation, actions, input suppression, and modifier keys.
 */

function TestComponent({ shortcuts }: { shortcuts: Shortcut[] }) {
  useKeyboardShortcuts(shortcuts);
  return <div data-testid="test">Test</div>;
}

function fireKey(
  key: string,
  options: Partial<KeyboardEventInit> = {},
  target?: HTMLElement,
) {
  const event = new KeyboardEvent("keydown", {
    key,
    bubbles: true,
    ...options,
  });
  (target ?? window).dispatchEvent(event);
}

beforeEach(() => {
  window.localStorage.clear();
});

describe("useKeyboardShortcuts", () => {
  it("j/k navigates list — calls handler", () => {
    const moveDown = vi.fn();
    const moveUp = vi.fn();

    render(
      <TestComponent
        shortcuts={[
          { key: "j", description: "Down", handler: moveDown },
          { key: "k", description: "Up", handler: moveUp },
        ]}
      />,
    );

    act(() => fireKey("j"));
    expect(moveDown).toHaveBeenCalledTimes(1);

    act(() => fireKey("k"));
    expect(moveUp).toHaveBeenCalledTimes(1);
  });

  it("h/l expands and collapses tree", () => {
    const expand = vi.fn();
    const collapse = vi.fn();

    render(
      <TestComponent
        shortcuts={[
          { key: "l", description: "Expand", handler: expand },
          { key: "h", description: "Collapse", handler: collapse },
        ]}
      />,
    );

    act(() => fireKey("l"));
    expect(expand).toHaveBeenCalled();

    act(() => fireKey("h"));
    expect(collapse).toHaveBeenCalled();
  });

  it("Enter opens node", () => {
    const openNode = vi.fn();

    render(
      <TestComponent
        shortcuts={[
          { key: "Enter", description: "Open", handler: openNode },
        ]}
      />,
    );

    act(() => fireKey("Enter"));
    expect(openNode).toHaveBeenCalled();
  });

  it("Escape closes panel", () => {
    const goBack = vi.fn();

    render(
      <TestComponent
        shortcuts={[
          { key: "Escape", description: "Back", handler: goBack },
        ]}
      />,
    );

    act(() => fireKey("Escape"));
    expect(goBack).toHaveBeenCalled();
  });

  it("Tab cycles focus", () => {
    const cycleFocus = vi.fn();

    render(
      <TestComponent
        shortcuts={[
          { key: "Tab", description: "Cycle", handler: cycleFocus },
        ]}
      />,
    );

    act(() => fireKey("Tab"));
    expect(cycleFocus).toHaveBeenCalled();
  });

  it("c creates node", () => {
    const create = vi.fn();

    render(
      <TestComponent
        shortcuts={[
          { key: "c", description: "Create", handler: create },
        ]}
      />,
    );

    act(() => fireKey("c"));
    expect(create).toHaveBeenCalled();
  });

  it("x marks done", () => {
    const markDone = vi.fn();

    render(
      <TestComponent
        shortcuts={[
          { key: "x", description: "Done", handler: markDone },
        ]}
      />,
    );

    act(() => fireKey("x"));
    expect(markDone).toHaveBeenCalled();
  });

  it("p opens prompt editor", () => {
    const editPrompt = vi.fn();

    render(
      <TestComponent
        shortcuts={[
          { key: "p", description: "Prompt", handler: editPrompt },
        ]}
      />,
    );

    act(() => fireKey("p"));
    expect(editPrompt).toHaveBeenCalled();
  });

  it("does NOT fire when focus is in an input", () => {
    const handler = vi.fn();

    const { container } = render(
      <>
        <TestComponent
          shortcuts={[{ key: "j", description: "Down", handler }]}
        />
        <input data-testid="input" />
      </>,
    );

    const input = container.querySelector("input")!;
    input.focus();

    act(() => {
      const event = new KeyboardEvent("keydown", {
        key: "j",
        bubbles: true,
      });
      Object.defineProperty(event, "target", { value: input });
      window.dispatchEvent(event);
    });

    expect(handler).not.toHaveBeenCalled();
  });

  it("Cmd+/ shows shortcut overlay", () => {
    const showOverlay = vi.fn();

    render(
      <TestComponent
        shortcuts={[
          {
            key: "/",
            meta: true,
            description: "Shortcuts",
            handler: showOverlay,
          },
        ]}
      />,
    );

    act(() => fireKey("/", { metaKey: true }));
    expect(showOverlay).toHaveBeenCalled();
  });

  it("priority keys 1-5 set correct priority", () => {
    const setPriority = vi.fn();

    render(
      <TestComponent
        shortcuts={[
          { key: "1", description: "P1", handler: () => setPriority(1) },
          { key: "2", description: "P2", handler: () => setPriority(2) },
          { key: "3", description: "P3", handler: () => setPriority(3) },
          { key: "4", description: "P4", handler: () => setPriority(4) },
          { key: "5", description: "P5", handler: () => setPriority(5) },
        ]}
      />,
    );

    act(() => fireKey("1"));
    expect(setPriority).toHaveBeenCalledWith(1);

    act(() => fireKey("3"));
    expect(setPriority).toHaveBeenCalledWith(3);

    act(() => fireKey("5"));
    expect(setPriority).toHaveBeenCalledWith(5);
  });
});
