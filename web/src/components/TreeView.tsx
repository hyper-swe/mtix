import { useCallback, useRef, useEffect, useState } from "react";
import type { TreeItem } from "../hooks/useNodeStore";
import { StatusIcon } from "./StatusIcon";
import { useTheme } from "../contexts/ThemeContext";

interface TreeViewProps {
  items: TreeItem[];
  selectedId: string | null;
  onSelect: (nodeId: string) => void;
  onToggleExpand: (nodeId: string) => void;
  onDragReparent?: (nodeId: string, newParentId: string) => void;
}

/** Row height per density: comfortable=32, compact=26. */
const ROW_HEIGHTS = { comfortable: 32, compact: 26 } as const;

/** Overscan rows for virtualization. */
const OVERSCAN = 10;

/**
 * Virtualized tree view for the sidebar per FR-UI-2.
 * Supports 10K+ nodes without lag via windowing.
 * Shows hierarchical structure with indent guides and expand/collapse.
 */
export function TreeView({
  items,
  selectedId,
  onSelect,
  onToggleExpand,
  onDragReparent,
}: TreeViewProps) {
  const { density } = useTheme();
  const rowHeight = ROW_HEIGHTS[density];
  const containerRef = useRef<HTMLDivElement>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [containerHeight, setContainerHeight] = useState(600);

  // Track container height.
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) {
        setContainerHeight(entry.contentRect.height);
      }
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const handleScroll = useCallback(() => {
    if (containerRef.current) {
      setScrollTop(containerRef.current.scrollTop);
    }
  }, []);

  // Compute visible range with overscan.
  const totalHeight = items.length * rowHeight;
  const startIdx = Math.max(0, Math.floor(scrollTop / rowHeight) - OVERSCAN);
  const endIdx = Math.min(
    items.length,
    Math.ceil((scrollTop + containerHeight) / rowHeight) + OVERSCAN,
  );
  const visibleItems = items.slice(startIdx, endIdx);
  const offsetY = startIdx * rowHeight;

  // Drag state.
  const [dragOverId, setDragOverId] = useState<string | null>(null);

  const handleDragStart = useCallback(
    (e: React.DragEvent, nodeId: string) => {
      e.dataTransfer.setData("text/plain", nodeId);
      e.dataTransfer.effectAllowed = "move";
    },
    [],
  );

  const handleDragOver = useCallback(
    (e: React.DragEvent, nodeId: string) => {
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
      setDragOverId(nodeId);
    },
    [],
  );

  const handleDragLeave = useCallback(() => {
    setDragOverId(null);
  }, []);

  const handleDrop = useCallback(
    (e: React.DragEvent, targetId: string) => {
      e.preventDefault();
      const sourceId = e.dataTransfer.getData("text/plain");
      setDragOverId(null);
      if (sourceId && sourceId !== targetId && onDragReparent) {
        onDragReparent(sourceId, targetId);
      }
    },
    [onDragReparent],
  );

  return (
    <div
      ref={containerRef}
      className="overflow-y-auto flex-1"
      onScroll={handleScroll}
      role="tree"
      aria-label="Node tree"
    >
      <div style={{ height: totalHeight, position: "relative" }}>
        <div
          style={{
            position: "absolute",
            top: offsetY,
            left: 0,
            right: 0,
          }}
        >
          {visibleItems.map((item) => (
            <TreeRow
              key={item.node.id}
              item={item}
              rowHeight={rowHeight}
              isSelected={item.node.id === selectedId}
              isDragOver={item.node.id === dragOverId}
              onSelect={onSelect}
              onToggleExpand={onToggleExpand}
              onDragStart={handleDragStart}
              onDragOver={handleDragOver}
              onDragLeave={handleDragLeave}
              onDrop={handleDrop}
            />
          ))}
        </div>
      </div>
    </div>
  );
}

interface TreeRowProps {
  item: TreeItem;
  rowHeight: number;
  isSelected: boolean;
  isDragOver: boolean;
  onSelect: (id: string) => void;
  onToggleExpand: (id: string) => void;
  onDragStart: (e: React.DragEvent, id: string) => void;
  onDragOver: (e: React.DragEvent, id: string) => void;
  onDragLeave: () => void;
  onDrop: (e: React.DragEvent, id: string) => void;
}

function TreeRow({
  item,
  rowHeight,
  isSelected,
  isDragOver,
  onSelect,
  onToggleExpand,
  onDragStart,
  onDragOver,
  onDragLeave,
  onDrop,
}: TreeRowProps) {
  const { node, depth, isExpanded, hasChildren, isLoadingChildren } = item;
  const indent = depth * 18 + 4;
  const [hovered, setHovered] = useState(false);

  return (
    <div
      role="treeitem"
      aria-expanded={hasChildren ? isExpanded : undefined}
      aria-selected={isSelected}
      aria-level={depth + 1}
      className="flex items-center cursor-pointer select-none"
      style={{
        height: rowHeight,
        paddingLeft: indent,
        paddingRight: 8,
        backgroundColor: isSelected
          ? "var(--color-accent-muted)"
          : isDragOver
            ? "var(--color-accent-subtle)"
            : hovered
              ? "var(--color-hover)"
              : "transparent",
        borderRadius: "var(--radius-sm)",
        margin: "0 4px",
      }}
      onClick={() => onSelect(node.id)}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      draggable
      onDragStart={(e) => onDragStart(e, node.id)}
      onDragOver={(e) => onDragOver(e, node.id)}
      onDragLeave={onDragLeave}
      onDrop={(e) => onDrop(e, node.id)}
    >
      {/* Indent guides */}
      {depth > 0 && (
        <div className="flex items-center" style={{ marginRight: 2 }}>
          {Array.from({ length: depth }).map((_, i) => (
            <div
              key={i}
              className="w-px h-full self-stretch"
              style={{
                backgroundColor: i === depth - 1 ? "var(--color-border)" : "transparent",
                marginLeft: 0,
                marginRight: 0,
                minHeight: rowHeight,
              }}
            />
          ))}
        </div>
      )}

      {/* Expand/collapse toggle */}
      {hasChildren ? (
        <button
          className="w-4 h-4 flex items-center justify-center mr-1 flex-shrink-0 rounded"
          style={{
            color: "var(--color-text-tertiary)",
          }}
          onClick={(e) => {
            e.stopPropagation();
            onToggleExpand(node.id);
          }}
          onMouseEnter={(e) => (e.currentTarget.style.backgroundColor = "var(--color-active)")}
          onMouseLeave={(e) => (e.currentTarget.style.backgroundColor = "transparent")}
          aria-label={isExpanded ? "Collapse" : "Expand"}
        >
          {isLoadingChildren ? (
            <LoadingDot />
          ) : (
            <ChevronIcon expanded={isExpanded} />
          )}
        </button>
      ) : (
        <span className="w-4 h-4 mr-1 flex-shrink-0" />
      )}

      {/* Status icon */}
      <span className="mr-1.5 flex-shrink-0">
        <StatusIcon status={node.status} size={12} />
      </span>

      {/* Node ID (short) */}
      <span
        className="text-[10px] font-mono mr-1.5 flex-shrink-0"
        style={{
          color: isSelected ? "var(--color-accent)" : "var(--color-text-tertiary)",
        }}
      >
        {shortId(node.id)}
      </span>

      {/* Title */}
      <span
        className="text-[13px] truncate flex-1"
        style={{
          color: isSelected ? "var(--color-text-primary)" : "var(--color-text-secondary)",
          fontWeight: isSelected ? 500 : 400,
          textDecoration:
            node.status === "done" || node.status === "cancelled"
              ? "line-through"
              : "none",
          opacity:
            node.status === "done" || node.status === "cancelled" ? 0.5 : 1,
        }}
        title={node.title}
      >
        {node.title}
      </span>

      {/* Progress for parent nodes */}
      {node.child_count > 0 && (
        <span
          className="text-[10px] ml-1 flex-shrink-0 tabular-nums"
          style={{ color: "var(--color-text-tertiary)" }}
        >
          {Math.round(node.progress * 100)}%
        </span>
      )}
    </div>
  );
}

/** Extract short form of dot-notation ID (last segment). */
function shortId(id: string): string {
  const parts = id.split(".");
  if (parts.length <= 2) return id;
  return "." + parts.slice(-1)[0];
}

function ChevronIcon({ expanded }: { expanded: boolean }) {
  return (
    <svg
      width="10"
      height="10"
      viewBox="0 0 10 10"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      style={{
        transform: expanded ? "rotate(90deg)" : "rotate(0deg)",
        transition: "transform var(--transition-fast)",
      }}
    >
      <path d="M3 2L7 5L3 8" />
    </svg>
  );
}

function LoadingDot() {
  return (
    <div
      className="w-2 h-2 rounded-full"
      style={{
        backgroundColor: "var(--color-text-tertiary)",
        animation: "pulse-dot 1s ease-in-out infinite",
      }}
    />
  );
}
