import {
  layoutDependencyGraph,
  type DependencyGraphLayoutRequest,
  type DependencyGraphLayoutResponse,
} from "./graph-layout";

type LayoutWorkerScope = {
  addEventListener: (type: "message", listener: (event: MessageEvent<DependencyGraphLayoutRequest>) => void) => void;
  postMessage: (message: DependencyGraphLayoutResponse) => void;
};

const workerScope = self as unknown as LayoutWorkerScope;

workerScope.addEventListener("message", (event) => {
  const { requestId, tasks } = event.data;
  if (!Number.isSafeInteger(requestId) || !Array.isArray(tasks)) return;

  try {
    workerScope.postMessage({ requestId, layout: layoutDependencyGraph(tasks) });
  } catch (error) {
    workerScope.postMessage({
      requestId,
      error: error instanceof Error ? error.message : "Dependency graph layout failed.",
    });
  }
});
