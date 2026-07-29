import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  buildNetcattyMonacoThemeColors,
  type NetcattyEditorColors,
} from './netcattyMonacoTheme';

const sampleColors: NetcattyEditorColors = {
  bg: '#1e1e1e',
  fg: '#d4d4d4',
  primary: '#569cd6',
  card: '#252526',
  mutedFg: '#858585',
  border: '#3c3c3c',
};

test('buildNetcattyMonacoThemeColors softens matching bracket highlight', () => {
  const colors = buildNetcattyMonacoThemeColors(sampleColors);

  assert.equal(colors['editorBracketMatch.background'], '#569cd614');
  assert.equal(colors['editorBracketMatch.border'], '#569cd640');
  // Keep bracket chrome lighter than selection so it does not compete with text.
  assert.equal(colors['editor.selectionBackground'], '#569cd640');
});
