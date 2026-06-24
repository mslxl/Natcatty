import type { Terminal as XTerm } from "@xterm/xterm";

import {
  createSyncBlockFilterState,
  filterSyncBlockClears,
  type SyncBlockFilterState,
} from "./filterSyncBlockClears.ts";

const syncBlockFilterStates = new WeakMap<XTerm, SyncBlockFilterState>();

export const resetTerminalSyncBlockFilter = (term: XTerm): void => {
  syncBlockFilterStates.set(term, createSyncBlockFilterState());
};

const getSyncBlockFilterState = (term: XTerm): SyncBlockFilterState => {
  let state = syncBlockFilterStates.get(term);
  if (!state) {
    state = createSyncBlockFilterState();
    syncBlockFilterStates.set(term, state);
  }
  return state;
};

export const filterTerminalSessionData = (term: XTerm, data: string): string =>
  filterSyncBlockClears(data, getSyncBlockFilterState(term));
