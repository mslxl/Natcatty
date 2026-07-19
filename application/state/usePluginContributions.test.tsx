import assert from 'node:assert/strict';
import test from 'node:test';
import { renderToStaticMarkup } from 'react-dom/server';

import { failClosedPluginContributionLoad, usePluginContributions } from './usePluginContributions';

function Probe() {
  const contributions = usePluginContributions();
  return (
    <span>
      {contributions.available ? 'available' : 'unavailable'}:{contributions.snapshot.plugins.length}
    </span>
  );
}

test('plugin contributions fail closed during server rendering', () => {
  assert.equal(renderToStaticMarkup(<Probe />), '<span>unavailable:0</span>');
});

test('plugin contribution refresh failures discard the last successful snapshot', () => {
  const failure = failClosedPluginContributionLoad('bridge unavailable');
  assert.equal(failure.available, false);
  assert.equal(failure.snapshot.plugins.length, 0);
  assert.match(failure.error.message, /bridge unavailable/u);
});
