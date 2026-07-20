import assert from 'node:assert/strict';
import test from 'node:test';

import { registerPluginTerminalLinkProvider } from './pluginTerminalLinkProvider.ts';

test('plugin terminal link host requests bounded line links and honors activation policy', async () => {
  let provider: { provideLinks(line: number, callback: (links?: unknown[]) => void): void } | undefined;
  let registrationDisposed = false;
  const requests: Array<[string, string, unknown, string | undefined]> = [];
  const opened: string[] = [];
  const term = {
    element: undefined,
    buffer: {
      active: {
        getLine(line: number) {
          if (line !== 0) return undefined;
          return { translateToString() { return 'visit example'; } };
        },
      },
    },
    registerLinkProvider(next: typeof provider) {
      provider = next;
      return { dispose() { registrationDisposed = true; } };
    },
  };
  const disposable = registerPluginTerminalLinkProvider({
    term: term as never,
    async request(kind, operation, payload, _deadlineMs, supersessionKey) {
      requests.push([kind, operation, payload, supersessionKey]);
      if (kind === 'terminal.link') {
        return {
          stale: false,
          results: [{
            providerId: 'com.example.links',
            status: 'ok',
            result: { links: [{ start: 6, length: 7, uri: 'https://example.com', label: 'Example' }] },
          }],
        };
      }
      return { stale: false, results: [] };
    },
    canActivate: (event) => event.metaKey,
    async openExternal(uri) { opened.push(uri); },
  });

  const links = await new Promise<unknown[]>((resolve) => {
    provider?.provideLinks(1, (value) => resolve(value ?? []));
  }) as Array<{ range: unknown; text: string; activate(event: { metaKey: boolean }): void }>;

  assert.deepEqual(requests.map(([kind, operation]) => [kind, operation]), [
    ['terminal.link', 'provideLinks'],
    ['terminal.hover', 'provideHovers'],
  ]);
  assert.deepEqual(requests.map((request) => request[3]), ['line:1', 'line:1']);
  assert.equal(links[0]?.text, 'example');
  assert.deepEqual(links[0]?.range, {
    start: { x: 7, y: 1 },
    end: { x: 13, y: 1 },
  });
  links[0]?.activate({ metaKey: false });
  links[0]?.activate({ metaKey: true });
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(opened, ['https://example.com']);

  disposable.dispose();
  assert.equal(registrationDisposed, true);
});

test('plugin terminal link host suppresses stale and oversized physical lines', async () => {
  let provider: { provideLinks(line: number, callback: (links?: unknown[]) => void): void } | undefined;
  let requestCount = 0;
  const term = {
    element: undefined,
    buffer: { active: { getLine() { return { translateToString() { return 'x'.repeat(8_193); } }; } } },
    registerLinkProvider(next: typeof provider) {
      provider = next;
      return { dispose() {} };
    },
  };
  registerPluginTerminalLinkProvider({
    term: term as never,
    async request() {
      requestCount += 1;
      return { stale: true, results: [] };
    },
    canActivate: () => true,
    async openExternal() {},
  });

  const links = await new Promise<unknown[]>((resolve) => {
    provider?.provideLinks(1, (value) => resolve(value ?? []));
  });
  assert.deepEqual(links, []);
  assert.equal(requestCount, 0);
});

test('plugin terminal link host releases xterm when activation or permission work stalls', async () => {
  let provider: { provideLinks(line: number, callback: (links?: unknown[]) => void): void } | undefined;
  const signals: AbortSignal[] = [];
  const term = {
    element: undefined,
    buffer: { active: { getLine() { return { translateToString() { return 'example'; } }; } } },
    registerLinkProvider(next: typeof provider) {
      provider = next;
      return { dispose() {} };
    },
  };
  registerPluginTerminalLinkProvider({
    term: term as never,
    request: (_kind, _operation, _payload, _deadlineMs, _supersessionKey, signal) => {
      if (signal) signals.push(signal);
      return new Promise(() => {});
    },
    canActivate: () => true,
    async openExternal() {},
    responseTimeoutMs: 5,
  });

  const links = await new Promise<unknown[]>((resolve) => {
    provider?.provideLinks(1, (value) => resolve(value ?? []));
  });
  assert.deepEqual(links, []);
  assert.equal(signals.length, 2);
  assert.equal(signals.every((signal) => signal.aborted), true);
});

test('plugin terminal link host performs no request when neither Provider kind is declared', async () => {
  let provider: { provideLinks(line: number, callback: (links?: unknown[]) => void): void } | undefined;
  let requests = 0;
  const term = {
    element: undefined,
    buffer: { active: { getLine() { return { translateToString() { return 'example'; } }; } } },
    registerLinkProvider(next: typeof provider) {
      provider = next;
      return { dispose() {} };
    },
  };
  registerPluginTerminalLinkProvider({
    term: term as never,
    async request() { requests += 1; return { stale: false, results: [] }; },
    isProviderAvailable: () => false,
    canActivate: () => true,
    async openExternal() {},
  });
  const links = await new Promise<unknown[]>((resolve) => {
    provider?.provideLinks(1, (value) => resolve(value ?? []));
  });
  assert.deepEqual(links, []);
  assert.equal(requests, 0);
});
