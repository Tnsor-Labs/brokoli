import type { Node, Edge, Position } from "./types";

/** Compute a cubic bezier path between two points */
export function edgePath(from: Position, to: Position): string {
  const dx = Math.abs(to.x - from.x) * 0.5;
  return `M ${from.x} ${from.y} C ${from.x + dx} ${from.y}, ${to.x - dx} ${to.y}, ${to.x} ${to.y}`;
}

/** Default node dimensions */
export const NODE_WIDTH = 200;
export const NODE_HEIGHT = 60;
export const PORT_RADIUS = 6;

/** Get output port position (right edge center) */
export function outputPort(node: Node): Position {
  return {
    x: node.position.x + NODE_WIDTH,
    y: node.position.y + NODE_HEIGHT / 2,
  };
}

/** Get input port position (left edge center) */
export function inputPort(node: Node): Position {
  return {
    x: node.position.x,
    y: node.position.y + NODE_HEIGHT / 2,
  };
}

/** Auto-layout nodes in a left-to-right flow */
export function autoLayout(nodes: Node[], edges: Edge[]): Node[] {
  if (nodes.length === 0) return nodes;

  // Build adjacency and in-degree
  const inDegree = new Map<string, number>();
  const adj = new Map<string, string[]>();
  for (const n of nodes) {
    inDegree.set(n.id, 0);
    adj.set(n.id, []);
  }
  for (const e of edges) {
    adj.get(e.from)?.push(e.to);
    inDegree.set(e.to, (inDegree.get(e.to) || 0) + 1);
  }

  // Topological sort into layers
  const layers: string[][] = [];
  let queue = nodes.filter((n) => (inDegree.get(n.id) || 0) === 0).map((n) => n.id);
  const assigned = new Set<string>();

  while (queue.length > 0) {
    layers.push([...queue]);
    queue.forEach((id) => assigned.add(id));

    const next: string[] = [];
    for (const id of queue) {
      for (const to of adj.get(id) || []) {
        inDegree.set(to, (inDegree.get(to) || 0) - 1);
        if ((inDegree.get(to) || 0) === 0 && !assigned.has(to)) {
          next.push(to);
        }
      }
    }
    queue = next;
  }

  // Assign positions
  const nodeMap = new Map(nodes.map((n) => [n.id, { ...n }]));
  const xGap = NODE_WIDTH + 80;
  const yGap = NODE_HEIGHT + 40;

  for (let col = 0; col < layers.length; col++) {
    const layer = layers[col];
    const totalHeight = layer.length * NODE_HEIGHT + (layer.length - 1) * 40;
    const startY = Math.max(40, (400 - totalHeight) / 2);

    for (let row = 0; row < layer.length; row++) {
      const node = nodeMap.get(layer[row]);
      if (node) {
        node.position = { x: 40 + col * xGap, y: startY + row * yGap };
      }
    }
  }

  return nodes.map((n) => nodeMap.get(n.id) || n);
}

/** Node type display config */
export const nodeTypeConfig: Record<string, { label: string; color: string }> = {
  source_file: { label: "File Source", color: "#3b82f6" },
  source_api: { label: "API Source", color: "#8b5cf6" },
  source_db: { label: "Database Source", color: "#06b6d4" },
  code: { label: "Python Code", color: "#eab308" },
  join: { label: "Join", color: "#14b8a6" },
  transform: { label: "Transform", color: "#f59e0b" },
  quality_check: { label: "Quality Check", color: "#22c55e" },
  sql_generate: { label: "SQL Generate", color: "#6366f1" },
  sink_file: { label: "File Output", color: "#ec4899" },
  sink_db: { label: "Database Sink", color: "#f97316" },
  sink_api: { label: "API Sink", color: "#a855f7" },
  migrate: { label: "DB Migration", color: "#0ea5e9" },
  condition: { label: "If/Else", color: "#f43f5e" },
};

/** Generate a unique node ID */
export function newNodeId(): string {
  return "n_" + Math.random().toString(36).slice(2, 8);
}
