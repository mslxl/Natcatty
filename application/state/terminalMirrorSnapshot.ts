import type { TerminalHibernateSnapshot } from "../../components/terminal/terminalHibernateRuntime";

export function mirrorSnapshotToHibernateSnapshot(
  mirror: { snapshot: string; alternateScreen: boolean },
): TerminalHibernateSnapshot {
  return {
    snapshot: mirror.snapshot,
    viewportSnapshot: mirror.snapshot,
    scrollbackSnapshot: "",
    alternateScreen: mirror.alternateScreen,
  };
}
