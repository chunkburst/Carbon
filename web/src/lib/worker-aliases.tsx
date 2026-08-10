/* eslint-disable react-refresh/only-export-components -- provider, hook, and formatter are one display-boundary module. */
import { createContext, useCallback, useContext, useMemo, type ReactNode } from "react";
import { useWorkerAliases } from "@/lib/queries";

export type WorkerAliasMap = Readonly<Record<string, string>>;

export function formatWorkerActor(actor?: string | null, aliases: WorkerAliasMap = {}): string {
  const canonical = actor?.trim() ?? "";
  if (!canonical) return "";
  const alias = aliases[canonical]?.trim();
  return alias ? `${alias} (${canonical})` : canonical;
}

type WorkerAliasContextValue = {
  aliases: WorkerAliasMap;
  formatWorker: (actor?: string | null) => string;
};

const EMPTY_CONTEXT: WorkerAliasContextValue = {
  aliases: {},
  formatWorker: (actor) => formatWorkerActor(actor),
};

const WorkerAliasContext = createContext<WorkerAliasContextValue>(EMPTY_CONTEXT);

// Aliases are home-wide user preferences. The actor string remains the canonical
// identity for all writes; this provider only changes the display layer.
export function WorkerAliasProvider({ home, children }: { home?: string; children: ReactNode }) {
  const aliasesQuery = useWorkerAliases(home);
  const aliases = aliasesQuery.data?.available ? aliasesQuery.data.data.aliases : EMPTY_CONTEXT.aliases;
  const formatWorker = useCallback((actor?: string | null) => formatWorkerActor(actor, aliases), [aliases]);
  const value = useMemo<WorkerAliasContextValue>(() => ({ aliases, formatWorker }), [aliases, formatWorker]);
  return <WorkerAliasContext.Provider value={value}>{children}</WorkerAliasContext.Provider>;
}

export function useWorkerAliasFormatter(): (actor?: string | null) => string {
  return useContext(WorkerAliasContext).formatWorker;
}

export function useWorkerAliasMap(): WorkerAliasMap {
  return useContext(WorkerAliasContext).aliases;
}
