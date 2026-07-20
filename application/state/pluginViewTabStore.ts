import { useSyncExternalStore } from 'react';

import { activeTabStore } from './activeTabStore';

export const PLUGIN_VIEW_TAB_PREFIX = 'plugin-view:';

export interface PluginViewTab {
  id: string;
  pluginId: string;
  pluginName: string;
  viewId: string;
  title: string;
  icon?: NetcattyPluginIconReference;
  context?: Record<string, unknown>;
}

export function toPluginViewTabId(pluginId: string, viewId: string): string {
  return `${PLUGIN_VIEW_TAB_PREFIX}${pluginId}:${viewId}`;
}

export function isPluginViewTabId(tabId: string): boolean {
  return tabId.startsWith(PLUGIN_VIEW_TAB_PREFIX);
}

export function resolvePluginViewRequest(
  requested: { viewId: string; context?: Record<string, unknown> } | null,
  activeTab: Pick<PluginViewTab, 'viewId' | 'context'> | null,
): { viewId: string; context?: Record<string, unknown> } | null {
  if (requested) return requested;
  return activeTab ? { viewId: activeTab.viewId, context: activeTab.context } : null;
}

export class PluginViewTabStore {
  private tabs: readonly PluginViewTab[] = Object.freeze([]);
  private listeners = new Set<() => void>();

  constructor(private readonly activeTabs: Pick<typeof activeTabStore, 'getActiveTabId' | 'setActiveTabId'> = activeTabStore) {}

  getTabs = () => this.tabs;

  getTab(tabId: string): PluginViewTab | undefined {
    return this.tabs.find((tab) => tab.id === tabId);
  }

  open(input: Omit<PluginViewTab, 'id'>): PluginViewTab {
    const id = toPluginViewTabId(input.pluginId, input.viewId);
    const tab = Object.freeze({ ...input, id });
    const index = this.tabs.findIndex((candidate) => candidate.id === id);
    this.tabs = Object.freeze(index === -1
      ? [...this.tabs, tab]
      : this.tabs.map((candidate) => candidate.id === id ? tab : candidate));
    this.emit();
    this.activeTabs.setActiveTabId(id);
    return tab;
  }

  close(tabId: string): void {
    const next = this.tabs.filter((tab) => tab.id !== tabId);
    if (next.length === this.tabs.length) return;
    this.tabs = Object.freeze(next);
    if (this.activeTabs.getActiveTabId() === tabId) this.activeTabs.setActiveTabId('vault');
    this.emit();
  }

  retain(viewIds: ReadonlySet<string>): void {
    const next = this.tabs.filter((tab) => viewIds.has(tab.viewId));
    if (next.length === this.tabs.length) return;
    const activeTabId = this.activeTabs.getActiveTabId();
    this.tabs = Object.freeze(next);
    if (isPluginViewTabId(activeTabId) && !next.some((tab) => tab.id === activeTabId)) {
      this.activeTabs.setActiveTabId('vault');
    }
    this.emit();
  }

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  private emit(): void {
    for (const listener of this.listeners) listener();
  }
}

export const pluginViewTabStore = new PluginViewTabStore();

export function usePluginViewTabs(): readonly PluginViewTab[] {
  return useSyncExternalStore(pluginViewTabStore.subscribe, pluginViewTabStore.getTabs, pluginViewTabStore.getTabs);
}
