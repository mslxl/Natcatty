import type {
  IDisposable,
  ILink,
  ILinkProvider,
  Terminal as XTerm,
} from '@xterm/xterm';

import {
  normalizePluginHoverResult,
  normalizePluginLinkResult,
  type PluginTerminalHover,
} from '../../domain/pluginTerminalProviders';
import {
  pluginTerminalCellRange,
  readPluginTerminalBufferText,
  type PluginTerminalBufferText,
} from './pluginTerminalBufferText';

const MAX_PROVIDER_LINE_CHARS = 8_192;
const PROVIDER_RESPONSE_TIMEOUT_MS = 800;

export interface PluginTerminalProviderCallResult {
  readonly providerId: string;
  readonly status: string;
  readonly result?: unknown;
}

export interface PluginTerminalProviderCallResponse {
  readonly stale: boolean;
  readonly results: readonly PluginTerminalProviderCallResult[];
}

export const ORDINARY_TERMINAL_PROVIDER_KINDS = Object.freeze([
  'terminal.completion',
  'terminal.decoration',
  'terminal.link',
  'terminal.hover',
  'terminal.matcher',
  'terminal.semantic',
  'terminal.prompt',
  'terminal.background',
] as const satisfies readonly NetcattyTerminalProviderKind[]);

export type RequestPluginTerminalProviders = (
  kind: NetcattyTerminalProviderKind,
  operation: string,
  payload: Readonly<Record<string, unknown>>,
  deadlineMs: number,
  supersessionKey?: string,
  signal?: AbortSignal,
) => Promise<PluginTerminalProviderCallResponse>;

function lineTextAt(term: XTerm, bufferLineNumber: number): PluginTerminalBufferText | null {
  const line = term.buffer.active.getLine(bufferLineNumber - 1);
  if (!line) return null;
  const text = readPluginTerminalBufferText(line, true);
  return text.text.length <= MAX_PROVIDER_LINE_CHARS ? text : null;
}

function matchingHover(
  hovers: readonly PluginTerminalHover[],
  providerId: string,
  start: number,
  length: number,
): PluginTerminalHover | undefined {
  return hovers.find((hover) => hover.providerId === providerId
    && hover.start === start
    && hover.length === length);
}

export async function waitForPluginTerminalProviderResponse(
  request: Promise<PluginTerminalProviderCallResponse>,
  timeoutMs: number,
  onTimeout?: () => void,
): Promise<PluginTerminalProviderCallResponse> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      request,
      new Promise<PluginTerminalProviderCallResponse>((resolve) => {
        timer = setTimeout(() => {
          onTimeout?.();
          resolve({ stale: false, results: Object.freeze([]) });
        }, timeoutMs);
      }),
    ]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}

function createTooltip(term: XTerm): {
  show(event: MouseEvent, contents: string): void;
  hide(): void;
  dispose(): void;
} {
  let tooltip: HTMLDivElement | null = null;
  const hide = () => {
    tooltip?.remove();
    tooltip = null;
  };
  return {
    show(event, contents) {
      hide();
      const root = term.element;
      if (!root) return;
      const bounds = root.getBoundingClientRect();
      tooltip = document.createElement('div');
      tooltip.className = 'xterm-hover pointer-events-none absolute z-50 max-h-[160px] max-w-[360px] overflow-hidden whitespace-pre-wrap break-words rounded-md border border-border bg-popover px-2 py-1 text-xs text-popover-foreground shadow-md';
      tooltip.setAttribute('role', 'tooltip');
      tooltip.textContent = contents;
      tooltip.style.left = `${Math.max(0, Math.min(event.clientX - bounds.left + 8, bounds.width - 368))}px`;
      tooltip.style.top = `${Math.max(0, Math.min(event.clientY - bounds.top + 12, bounds.height - 40))}px`;
      root.appendChild(tooltip);
    },
    hide,
    dispose: hide,
  };
}

export function registerPluginTerminalLinkProvider(options: {
  term: XTerm;
  request: RequestPluginTerminalProviders;
  canActivate(event: MouseEvent): boolean;
  openExternal(uri: string): Promise<void>;
  responseTimeoutMs?: number;
  isProviderAvailable?(kind: NetcattyTerminalProviderKind): boolean;
}): IDisposable {
  const tooltip = createTooltip(options.term);
  let disposed = false;
  const requestWithTimeout = (
    kind: NetcattyTerminalProviderKind,
    operation: string,
    payload: Readonly<Record<string, unknown>>,
    supersessionKey: string,
  ) => {
    const controller = new AbortController();
    return waitForPluginTerminalProviderResponse(
      options.request(kind, operation, payload, 750, supersessionKey, controller.signal),
      options.responseTimeoutMs ?? PROVIDER_RESPONSE_TIMEOUT_MS,
      () => controller.abort(),
    );
  };
  const provider: ILinkProvider = {
    provideLinks(bufferLineNumber, callback) {
      const linkAvailable = options.isProviderAvailable?.('terminal.link') ?? true;
      const hoverAvailable = options.isProviderAvailable?.('terminal.hover') ?? true;
      if (!linkAvailable && !hoverAvailable) {
        callback(undefined);
        return;
      }
      const line = lineTextAt(options.term, bufferLineNumber);
      if (disposed || line == null || line.text.length === 0) {
        callback(undefined);
        return;
      }
      void Promise.all([
        linkAvailable
          ? requestWithTimeout(
              'terminal.link',
              'provideLinks',
              { line: line.text, bufferLineNumber },
              `line:${bufferLineNumber}`,
            )
          : Promise.resolve({ stale: false, results: Object.freeze([]) }),
        hoverAvailable
          ? requestWithTimeout(
              'terminal.hover',
              'provideHovers',
              { line: line.text, bufferLineNumber },
              `line:${bufferLineNumber}`,
            )
          : Promise.resolve({ stale: false, results: Object.freeze([]) }),
      ]).then(([linkResponse, hoverResponse]) => {
        if (disposed || linkResponse.stale || hoverResponse.stale) {
          callback(undefined);
          return;
        }
        const links = linkResponse.results.flatMap((result) => result.status === 'ok'
          ? normalizePluginLinkResult(result.providerId, result.result, line.text.length)
          : []);
        const hovers = hoverResponse.results.flatMap((result) => result.status === 'ok'
          ? normalizePluginHoverResult(result.providerId, result.result, line.text.length)
          : []);
        const consumedHoverKeys = new Set<string>();
        const result: ILink[] = links.flatMap((link) => {
          const cellRange = pluginTerminalCellRange(line, link.start, link.length);
          if (!cellRange) return [];
          const hover = matchingHover(hovers, link.providerId, link.start, link.length);
          if (hover) consumedHoverKeys.add(`${hover.providerId}\0${hover.start}\0${hover.length}`);
          const hoverText = [hover?.contents ?? link.label, link.uri].filter(Boolean).join('\n');
          return [{
            range: {
              start: { x: cellRange.x + 1, y: bufferLineNumber },
              end: { x: cellRange.x + cellRange.width, y: bufferLineNumber },
            },
            text: line.text.slice(link.start, link.start + link.length),
            decorations: { pointerCursor: true, underline: true },
            activate: (event) => {
              if (options.canActivate(event)) void options.openExternal(link.uri).catch(() => {});
            },
            ...(hoverText ? {
              hover: (event: MouseEvent) => tooltip.show(event, hoverText),
              leave: () => tooltip.hide(),
            } : {}),
          }];
        });
        for (const hover of hovers) {
          if (consumedHoverKeys.has(`${hover.providerId}\0${hover.start}\0${hover.length}`)) continue;
          const cellRange = pluginTerminalCellRange(line, hover.start, hover.length);
          if (!cellRange) continue;
          result.push({
            range: {
              start: { x: cellRange.x + 1, y: bufferLineNumber },
              end: { x: cellRange.x + cellRange.width, y: bufferLineNumber },
            },
            text: line.text.slice(hover.start, hover.start + hover.length),
            decorations: { pointerCursor: false, underline: false },
            activate: () => {},
            hover: (event: MouseEvent) => tooltip.show(event, hover.contents),
            leave: () => tooltip.hide(),
          });
        }
        callback(result.length > 0 ? result : undefined);
      }).catch(() => callback(undefined));
    },
  };
  const registration = options.term.registerLinkProvider(provider);
  return {
    dispose() {
      if (disposed) return;
      disposed = true;
      tooltip.dispose();
      registration.dispose();
    },
  };
}
