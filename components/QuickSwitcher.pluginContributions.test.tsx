import assert from 'node:assert/strict';
import test from 'node:test';

import { buildPluginPaletteItems } from './QuickSwitcher';

function pluginSnapshot(menuEnabled: boolean): NetcattyPluginContributionSnapshot['plugins'] {
  return [{
    id: 'com.example.palette',
    version: '1.0.0',
    displayName: 'Palette plugin',
    description: '',
    commands: [{
      id: 'com.example.palette.run',
      title: 'Run command',
      enabled: true,
    }],
    keybindings: [],
    menus: [{
      id: 'com.example.palette:menu:0',
      command: 'com.example.palette.run',
      alt: 'com.example.palette.runAlternate',
      location: 'commandPalette',
      title: 'Run from palette',
      visible: true,
      enabled: menuEnabled,
      shortcut: 'ctrl+shift+r',
    }],
    settings: [],
    views: [],
  }];
}

test('plugin palette items preserve menu-specific enablement', () => {
  assert.deepEqual(buildPluginPaletteItems(pluginSnapshot(false), ''), [{
    type: 'plugin-command',
    id: 'com.example.palette.run',
    title: 'Run from palette',
    pluginTitle: 'Palette plugin',
    enabled: false,
    altCommand: 'com.example.palette.runAlternate',
    shortcut: 'ctrl+shift+r',
  }]);
  assert.equal(buildPluginPaletteItems(pluginSnapshot(true), '')[0]?.enabled, true);
});
