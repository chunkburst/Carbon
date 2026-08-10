import dagre from "@dagrejs/dagre";

/**
 * Geometry is shared by Dagre and the React Flow task card. Keeping it here makes
 * the layout deterministic and lets callers validate it without rendering a canvas.
 */
export const GRAPH_NODE_WIDTH = 230;
export const GRAPH_NODE_HEIGHT = 46;
/** Large connected islands use a Worker so Dagre cannot block graph interaction. */
export const DEPENDENCY_LAYOUT_WORKER_THRESHOLD = 240;

const RANK_NODE_GAP = 48;
const RANK_GAP = 104;
const RANK_BREATHING_ROOM = 32;
const COMPONENT_GAP_X = 144;
const COMPONENT_GAP_Y = 112;
const GRAPH_OUTER_MARGIN = 72;
const PREFERRED_CANVAS_ASPECT = 1.45;

export type DependencyTask = {
  id: string;
  deps?: readonly string[];
};

export type DependencyConnection = {
  source: string;
  target: string;
  count: number;
};

export type GraphPosition = {
  x: number;
  y: number;
};

export type GraphComponent = {
  taskIds: string[];
  nodes: Array<GraphPosition & { id: string }>;
  width: number;
  height: number;
};

export type PackedGraphComponent = GraphComponent & GraphPosition;

export type DependencyGraphLayout = {
  positions: Map<string, GraphPosition>;
  connections: DependencyConnection[];
  components: PackedGraphComponent[];
};

/** Structured-clone-safe request/response shapes shared by the UI and layout Worker. */
export type DependencyGraphLayoutRequest = {
  requestId: number;
  tasks: DependencyTask[];
};

export type DependencyGraphLayoutResponse = {
  requestId: number;
  layout?: DependencyGraphLayout;
  error?: string;
};

export type DependencyGraphPartition<T extends DependencyTask> = {
  tasks: T[];
  connected: T[];
  isolated: T[];
  connections: DependencyConnection[];
};

export function shouldUseDependencyLayoutWorker(connectedNodeCount: number): boolean {
  return connectedNodeCount > DEPENDENCY_LAYOUT_WORKER_THRESHOLD;
}

/** A stable signature for layout and viewport work; task presentation changes do not affect it. */
export function dependencyTopologyKey<T extends DependencyTask>(input: readonly T[]): string {
  const tasks = uniqueDependencyTasks(input);
  return JSON.stringify({
    ids: tasks.map((task) => task.id),
    edges: collectDependencyConnections(tasks).map(({ source, target }) => [source, target]),
  });
}

/**
 * React Flow cannot render duplicate node IDs. The server should never return them,
 * but this makes graph layout safe while an external source is being migrated.
 */
export function uniqueDependencyTasks<T extends DependencyTask>(tasks: readonly T[]): T[] {
  const seen = new Set<string>();
  const unique: T[] = [];
  for (const task of tasks) {
    if (!task.id || seen.has(task.id)) continue;
    seen.add(task.id);
    unique.push(task);
  }
  return unique.sort((left, right) => left.id.localeCompare(right.id));
}

/** Collect valid directed dependencies once, while retaining duplicate-dependency counts for the UI. */
export function collectDependencyConnections<T extends DependencyTask>(
  tasks: readonly T[],
): DependencyConnection[] {
  const ids = new Set(tasks.map((task) => task.id));
  const connections = new Map<string, DependencyConnection>();

  for (const task of tasks) {
    for (const source of task.deps ?? []) {
      if (!ids.has(source)) continue;
      // NUL is not valid in a Carbon task id and avoids ambiguity from printable separators.
      const key = `${source}\u0000${task.id}`;
      const current = connections.get(key);
      if (current) {
        current.count += 1;
      } else {
        connections.set(key, { source, target: task.id, count: 1 });
      }
    }
  }

  return [...connections.values()].sort(
    (left, right) => left.source.localeCompare(right.source) || left.target.localeCompare(right.target),
  );
}

/**
 * Separates dependency-bearing tasks from the common high-volume case: tasks with
 * no valid in-scope edge. The UI can keep the actual dependency graph readable and
 * render unlinked work as a searchable list instead of shrinking every card into a
 * single enormous React Flow viewport.
 */
export function partitionDependencyTasks<T extends DependencyTask>(
  input: readonly T[],
): DependencyGraphPartition<T> {
  const tasks = uniqueDependencyTasks(input);
  const connections = collectDependencyConnections(tasks);
  const connectedIds = new Set<string>();
  for (const connection of connections) {
    connectedIds.add(connection.source);
    connectedIds.add(connection.target);
  }

  return {
    tasks,
    connected: tasks.filter((task) => connectedIds.has(task.id)),
    isolated: tasks.filter((task) => !connectedIds.has(task.id)),
    connections,
  };
}

/**
 * Splits the dependency graph as an undirected graph: a dependency's direction
 * matters to Dagre, but not to whether two cards should share a visual island.
 */
export function getConnectedTaskComponents<T extends DependencyTask>(tasks: readonly T[]): T[][] {
  const uniqueTasks = uniqueDependencyTasks(tasks);
  const taskById = new Map(uniqueTasks.map((task) => [task.id, task]));
  const neighbours = new Map(uniqueTasks.map((task) => [task.id, new Set<string>()]));

  for (const connection of collectDependencyConnections(uniqueTasks)) {
    neighbours.get(connection.source)?.add(connection.target);
    neighbours.get(connection.target)?.add(connection.source);
  }

  const visited = new Set<string>();
  const components: T[][] = [];
  for (const task of uniqueTasks) {
    if (visited.has(task.id)) continue;

    const componentIds: string[] = [];
    const pending = [task.id];
    visited.add(task.id);
    while (pending.length > 0) {
      const current = pending.pop();
      if (!current) continue;
      componentIds.push(current);
      for (const neighbour of neighbours.get(current) ?? []) {
        if (visited.has(neighbour)) continue;
        visited.add(neighbour);
        pending.push(neighbour);
      }
    }

    components.push(
      componentIds
        .sort((left, right) => left.localeCompare(right))
        .map((id) => taskById.get(id) as T),
    );
  }

  return components;
}

/**
 * Give cards in the same rank a small, deterministic vertical floor. This prevents
 * custom labels from visually touching even when Dagre positions them very closely.
 */
function separateComponentRanks(nodes: Array<GraphPosition & { id: string }>) {
  const ranks = new Map<number, Array<GraphPosition & { id: string }>>();
  for (const node of nodes) {
    const rank = Math.round(node.x / 16) * 16;
    const rankNodes = ranks.get(rank) ?? [];
    rankNodes.push(node);
    ranks.set(rank, rankNodes);
  }

  const adjusted = new Map<string, GraphPosition>();
  for (const rankNodes of ranks.values()) {
    rankNodes.sort((a, b) => a.y - b.y || a.id.localeCompare(b.id));
    let floor = Number.NEGATIVE_INFINITY;
    for (const node of rankNodes) {
      const y = Math.max(node.y, floor);
      adjusted.set(node.id, { x: node.x, y });
      floor = y + GRAPH_NODE_HEIGHT + RANK_BREATHING_ROOM;
    }
  }

  return nodes.map((node) => ({ ...node, ...(adjusted.get(node.id) ?? node) }));
}

function layoutComponent<T extends DependencyTask>(
  tasks: readonly T[],
  connections: readonly DependencyConnection[],
): GraphComponent {
  // Isolated tasks are the common worst case for a large planning board. Running
  // Dagre once per singleton adds hundreds or thousands of avoidable graph-layout
  // passes, even though its geometry is already known exactly.
  if (tasks.length === 1) {
    return {
      taskIds: [tasks[0].id],
      nodes: [{ id: tasks[0].id, x: 0, y: 0 }],
      width: GRAPH_NODE_WIDTH,
      height: GRAPH_NODE_HEIGHT,
    };
  }

  const graph = new dagre.graphlib.Graph();
  graph.setDefaultEdgeLabel(() => ({}));
  graph.setGraph({
    rankdir: "LR",
    align: "UL",
    nodesep: RANK_NODE_GAP,
    ranksep: RANK_GAP,
    marginx: 0,
    marginy: 0,
    // Network simplex gives the most polished small/medium islands, but its main-
    // thread cost becomes visible for a very large component. Longest-path keeps
    // the dependency direction deterministic and makes the safety path close to
    // linear when an imported project contains hundreds of connected tasks.
    ranker: tasks.length > 240 ? "longest-path" : "network-simplex",
    acyclicer: "greedy",
  });

  for (const task of tasks) {
    graph.setNode(task.id, { width: GRAPH_NODE_WIDTH, height: GRAPH_NODE_HEIGHT });
  }
  for (const connection of connections) {
    graph.setEdge(connection.source, connection.target, { minlen: 1, weight: 3 });
  }
  dagre.layout(graph);

  const positioned = separateComponentRanks(tasks.map((task) => {
    const position = graph.node(task.id) as { x: number; y: number };
    return {
      id: task.id,
      x: position.x - GRAPH_NODE_WIDTH / 2,
      y: position.y - GRAPH_NODE_HEIGHT / 2,
    };
  }));

  const minX = Math.min(...positioned.map((node) => node.x));
  const minY = Math.min(...positioned.map((node) => node.y));
  const maxX = Math.max(...positioned.map((node) => node.x + GRAPH_NODE_WIDTH));
  const maxY = Math.max(...positioned.map((node) => node.y + GRAPH_NODE_HEIGHT));

  return {
    taskIds: tasks.map((task) => task.id),
    nodes: positioned.map((node) => ({ ...node, x: node.x - minX, y: node.y - minY })),
    width: maxX - minX,
    height: maxY - minY,
  };
}

type ComponentPackResult<T extends GraphComponent> = {
  items: Array<T & GraphPosition>;
  width: number;
  height: number;
};

function packInRows<T extends GraphComponent>(
  components: readonly T[],
  columns: number,
): ComponentPackResult<T> {
  const items: Array<T & GraphPosition> = [];
  let x = GRAPH_OUTER_MARGIN;
  let y = GRAPH_OUTER_MARGIN;
  let rowHeight = 0;
  let maxRight = GRAPH_OUTER_MARGIN;

  for (let index = 0; index < components.length; index += 1) {
    if (index > 0 && index % columns === 0) {
      y += rowHeight + COMPONENT_GAP_Y;
      x = GRAPH_OUTER_MARGIN;
      rowHeight = 0;
    }
    const component = components[index];
    if (index % columns !== 0) x += COMPONENT_GAP_X;

    items.push({ ...component, x, y });
    x += component.width;
    rowHeight = Math.max(rowHeight, component.height);
    maxRight = Math.max(maxRight, x);
  }

  return {
    items,
    width: maxRight + GRAPH_OUTER_MARGIN,
    height: y + rowHeight + GRAPH_OUTER_MARGIN,
  };
}

/**
 * Packs disconnected islands into a deterministic, screen-shaped grid. Candidate
 * column counts are scored by occupied area and closeness to a landscape canvas,
 * so the graph stays legible on normal and narrow panes without a long single row.
 */
export function packGraphComponents<T extends GraphComponent>(
  components: readonly T[],
): Array<T & GraphPosition> {
  if (components.length === 0) return [];

  const ordered = [...components].sort(
    (left, right) => right.height - left.height
      || right.width - left.width
      || left.taskIds[0].localeCompare(right.taskIds[0]),
  );
  const averageWidth = ordered.reduce((sum, component) => sum + component.width + COMPONENT_GAP_X, 0) / ordered.length;
  const averageHeight = ordered.reduce((sum, component) => sum + component.height + COMPONENT_GAP_Y, 0) / ordered.length;
  const preferredColumns = Math.max(
    1,
    Math.min(
      ordered.length,
      Math.round(Math.sqrt((ordered.length * PREFERRED_CANVAS_ASPECT * averageHeight) / averageWidth)),
    ),
  );
  // Score a constant-width neighborhood around the aspect-derived estimate. This keeps
  // packing linear after the O(N log N) deterministic component ordering rather than trying
  // every possible column count.
  const candidates = new Set<number>([1, ordered.length]);
  for (let offset = -4; offset <= 4; offset += 1) {
    candidates.add(Math.max(1, Math.min(ordered.length, preferredColumns + offset)));
  }

  let best: ComponentPackResult<T> | undefined;
  for (const columns of candidates) {
    const candidate = packInRows(ordered, columns);
    const aspect = candidate.width / Math.max(candidate.height, 1);
    const aspectPenalty = Math.abs(Math.log(aspect / PREFERRED_CANVAS_ASPECT));
    const score = candidate.width * candidate.height * (1 + aspectPenalty * 0.8);
    const bestScore = best
      ? best.width * best.height * (1 + Math.abs(Math.log((best.width / Math.max(best.height, 1)) / PREFERRED_CANVAS_ASPECT)) * 0.8)
      : Number.POSITIVE_INFINITY;
    if (score < bestScore) best = candidate;
  }

  return best?.items ?? [];
}

/**
 * Pure graph geometry entry point for the UI and future tests. Every disconnected
 * component is ranked independently, then moved into a padded two-dimensional grid.
 */
export function layoutDependencyGraph<T extends DependencyTask>(
  input: readonly T[],
): DependencyGraphLayout {
  const tasks = uniqueDependencyTasks(input);
  const connections = collectDependencyConnections(tasks);
  const connected = getConnectedTaskComponents(tasks);
  const componentByTask = new Map<string, number>();
  connected.forEach((component, index) => {
    component.forEach((task) => componentByTask.set(task.id, index));
  });
  const connectionsByComponent = connected.map((): DependencyConnection[] => []);
  for (const connection of connections) {
    const index = componentByTask.get(connection.source);
    if (index !== undefined && index === componentByTask.get(connection.target)) {
      connectionsByComponent[index].push(connection);
    }
  }
  const components = connected.map((component, index) => layoutComponent(component, connectionsByComponent[index]));
  const packedComponents = packGraphComponents(components);
  const positions = new Map<string, GraphPosition>();

  for (const component of packedComponents) {
    for (const node of component.nodes) {
      positions.set(node.id, { x: component.x + node.x, y: component.y + node.y });
    }
  }

  return { positions, connections, components: packedComponents };
}
