import assert from 'node:assert/strict';
import test from 'node:test';
import { renderToStaticMarkup } from 'react-dom/server';

import { usePluginContributions } from './usePluginContributions';

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
