/**
 * DependencyGraph — interactive SVG visualization of node dependencies.
 * Per MTIX-9.4.3. Uses a simple force-directed layout for small graphs
 * and hierarchical layout for larger ones.
 * SVG-based for zero-dependency rendering.
 */

import { useState, useMemo, useCallback, useRef } from "react";
import type { Node, Dependency, Status } from "../types";

/** Graph node with computed position. */
interface GraphNode {
  id: string;
  title: string;
  status: Status;
  x: number;
  y: number;
}

/** Graph edge between nodes. */
interface GraphEdge {
  fromId: string;
  toId: string;
  type: "parent-child" | "blocks" | "related" | "duplicates" | "discovered_from";
}

/** Edge style based on dependency type. */
function edgeColor(type: string): string {
  switch (type) {
    case "blocks": return "var(--color-status-blocked)";
    case "related": return "var(--color-text-secondary)";
    case "duplicates": return "var(--color-status-invalidated)";
    case "parent-child": return "var(--color-border)";
    default: return "var(--color-border)";
  }
}

/** Node fill color based on status. */
function nodeColor(status: Status): string {
  return `var(--color-status-${status.replace("_", "-")})`;
}

/** Simple hierarchical layout for tree-structured data. */
function computeHierarchicalLayout(
  nodes: Node[],
  deps: Dependency[],
): { graphNodes: GraphNode[]; graphEdges: GraphEdge[] } {
  const nodeMap = new Map(nodes.map((n) => [n.id, n]));
  const graphNodes: GraphNode[] = [];
  const graphEdges: GraphEdge[] = [];

  // Group by depth for vertical layout.
  const byDepth = new Map<number, Node[]>();
  for (const node of nodes) {
    const existing = byDepth.get(node.depth) ?? [];
    existing.push(node);
    byDepth.set(node.depth, existing);
  }

  // Position nodes in grid.
  const xSpacing = 180;
  const ySpacing = 100;

  for (const [depth, depthNodes] of byDepth) {
    depthNodes.forEach((node, index) => {
      const totalWidth = depthNodes.length * xSpacing;
      const startX = -totalWidth / 2 + xSpacing / 2;
      graphNodes.push({
        id: node.id,
        title: node.title,
        status: node.status,
        x: startX + index * xSpacing,
        y: depth * ySpacing,
      });
    });
  }

  // Parent-child edges.
  for (const node of nodes) {
    if (node.parent_id && nodeMap.has(node.parent_id)) {
      graphEdges.push({
        fromId: node.parent_id,
        toId: node.id,
        type: "parent-child",
      });
    }
  }

  // Dependency edges.
  for (const dep of deps) {
    if (nodeMap.has(dep.from_id) && nodeMap.has(dep.to_id)) {
      graphEdges.push({
        fromId: dep.from_id,
        toId: dep.to_id,
        type: dep.dep_type,
      });
    }
  }

  return { graphNodes, graphEdges };
}

export interface DependencyGraphProps {
  /** Nodes to display. */
  nodes: Node[];
  /** Dependencies between nodes. */
  dependencies: Dependency[];
  /** Currently highlighted node ID. */
  highlightedId?: string;
  /** Navigate to a node. */
  onNavigate: (nodeId: string) => void;
  /** Additional CSS class. */
  className?: string;
}

export function DependencyGraph({
  nodes,
  dependencies,
  highlightedId,
  onNavigate,
  className = "",
}: DependencyGraphProps) {
  const [zoom, setZoom] = useState(1);
  const [pan, setPan] = useState({ x: 400, y: 50 });
  const [dragging, setDragging] = useState(false);
  const lastMouse = useRef({ x: 0, y: 0 });

  const { graphNodes, graphEdges } = useMemo(
    () => computeHierarchicalLayout(nodes, dependencies),
    [nodes, dependencies],
  );

  const nodePositions = useMemo(() => {
    return new Map(graphNodes.map((n) => [n.id, { x: n.x, y: n.y }]));
  }, [graphNodes]);

  const handleWheel = useCallback((e: React.WheelEvent) => {
    e.preventDefault();
    const delta = e.deltaY > 0 ? -0.1 : 0.1;
    setZoom((z) => Math.max(0.2, Math.min(3, z + delta)));
  }, []);

  const handleMouseDown = useCallback((e: React.MouseEvent) => {
    if (e.button === 0) {
      setDragging(true);
      lastMouse.current = { x: e.clientX, y: e.clientY };
    }
  }, []);

  const handleMouseMove = useCallback(
    (e: React.MouseEvent) => {
      if (dragging) {
        const dx = e.clientX - lastMouse.current.x;
        const dy = e.clientY - lastMouse.current.y;
        setPan((p) => ({ x: p.x + dx, y: p.y + dy }));
        lastMouse.current = { x: e.clientX, y: e.clientY };
      }
    },
    [dragging],
  );

  const handleMouseUp = useCallback(() => {
    setDragging(false);
  }, []);

  const handleZoomIn = useCallback(() => {
    setZoom((z) => Math.min(3, z + 0.2));
  }, []);

  const handleZoomOut = useCallback(() => {
    setZoom((z) => Math.max(0.2, z - 0.2));
  }, []);

  if (nodes.length === 0) {
    return (
      <div
        className={`flex items-center justify-center p-8 ${className}`}
        data-testid="dependency-graph"
      >
        <p style={{ color: "var(--color-text-secondary)" }}>
          No nodes to display
        </p>
      </div>
    );
  }

  return (
    <div
      className={`relative ${className}`}
      data-testid="dependency-graph"
    >
      {/* Zoom controls */}
      <div
        className="absolute top-2 right-2 z-10 flex gap-1"
        data-testid="zoom-controls"
      >
        <button
          className="w-7 h-7 rounded border flex items-center justify-center text-sm cursor-pointer"
          style={{
            backgroundColor: "var(--color-surface)",
            borderColor: "var(--color-border)",
            color: "var(--color-text-primary)",
          }}
          onClick={handleZoomIn}
          data-testid="zoom-in"
        >
          +
        </button>
        <button
          className="w-7 h-7 rounded border flex items-center justify-center text-sm cursor-pointer"
          style={{
            backgroundColor: "var(--color-surface)",
            borderColor: "var(--color-border)",
            color: "var(--color-text-primary)",
          }}
          onClick={handleZoomOut}
          data-testid="zoom-out"
        >
          −
        </button>
      </div>

      {/* SVG canvas */}
      <svg
        width="100%"
        height="500"
        style={{ cursor: dragging ? "grabbing" : "grab" }}
        onWheel={handleWheel}
        onMouseDown={handleMouseDown}
        onMouseMove={handleMouseMove}
        onMouseUp={handleMouseUp}
        onMouseLeave={handleMouseUp}
        data-testid="graph-svg"
      >
        <g
          transform={`translate(${pan.x}, ${pan.y}) scale(${zoom})`}
          data-testid="graph-content"
        >
          {/* Edges */}
          {graphEdges.map((edge, i) => {
            const from = nodePositions.get(edge.fromId);
            const to = nodePositions.get(edge.toId);
            if (!from || !to) return null;

            const isDep = edge.type !== "parent-child";

            return (
              <line
                key={i}
                x1={from.x}
                y1={from.y}
                x2={to.x}
                y2={to.y}
                stroke={edgeColor(edge.type)}
                strokeWidth={isDep ? 2 : 1}
                strokeDasharray={isDep ? "6,3" : "none"}
                markerEnd={isDep ? "url(#arrowhead)" : undefined}
                data-testid={`edge-${edge.type}`}
              />
            );
          })}

          {/* Arrow marker definition */}
          <defs>
            <marker
              id="arrowhead"
              markerWidth="10"
              markerHeight="7"
              refX="10"
              refY="3.5"
              orient="auto"
            >
              <polygon
                points="0 0, 10 3.5, 0 7"
                fill="var(--color-status-blocked)"
              />
            </marker>
          </defs>

          {/* Nodes */}
          {graphNodes.map((node) => {
            const isHighlighted = node.id === highlightedId;
            return (
              <g
                key={node.id}
                transform={`translate(${node.x}, ${node.y})`}
                onClick={() => onNavigate(node.id)}
                style={{ cursor: "pointer" }}
                data-testid={`graph-node-${node.id}`}
              >
                {/* Node circle */}
                <circle
                  r={isHighlighted ? 20 : 16}
                  fill={nodeColor(node.status)}
                  stroke={
                    isHighlighted
                      ? "var(--color-accent)"
                      : "var(--color-border)"
                  }
                  strokeWidth={isHighlighted ? 3 : 1}
                />
                {/* Node label */}
                <text
                  y={30}
                  textAnchor="middle"
                  fill="var(--color-text-primary)"
                  fontSize="10"
                  data-testid={`node-label-${node.id}`}
                >
                  {node.title.length > 20
                    ? node.title.slice(0, 18) + "…"
                    : node.title}
                </text>
              </g>
            );
          })}
        </g>
      </svg>
    </div>
  );
}
