const MCP_TOOL_NAME_PREFIX = 'mcp__';

const CLI_ARTIFACT_TOOL_NAMES = new Map<string, string>([
  ['vault host get', 'host_get'],
]);

function readCommandString(args: Record<string, unknown> | undefined): string | null {
  if (!args) return null;
  const raw = args.command;
  if (typeof raw === 'string') return raw || null;
  if (!Array.isArray(raw) || raw.length === 0) return null;

  const isShellWrap =
    raw.length >= 3 &&
    /(?:^|\/)(sh|bash|zsh|fish|ash|dash)$/.test(String(raw[0] ?? '')) &&
    /^-l?c$/.test(String(raw[1] ?? ''));

  return isShellWrap
    ? String(raw[raw.length - 1] ?? '') || null
    : raw.map((part) => String(part)).join(' ');
}

function unwrapShellCommand(command: string): string {
  const strWrap = command.match(
    /^(?:\S*\/)?(?:sh|bash|zsh|fish|ash|dash)\s+-l?c\s+(['"])([\s\S]*)\1\s*$/,
  );
  return strWrap ? strWrap[2] : command;
}

function stripWrappingQuote(value: string): string {
  const trimmed = value.trim();
  if (trimmed.length < 2) return trimmed;
  const first = trimmed[0];
  const last = trimmed[trimmed.length - 1];
  return (first === last && (first === '"' || first === "'"))
    ? trimmed.slice(1, -1)
    : trimmed;
}

export function normalizeArtifactToolName(toolName: string | undefined): string | undefined {
  const trimmed = toolName?.trim();
  if (!trimmed) return undefined;

  if (trimmed.startsWith(MCP_TOOL_NAME_PREFIX)) {
    const segments = trimmed.split('__').filter(Boolean);
    return segments[segments.length - 1] || trimmed;
  }

  return trimmed;
}

export function inferArtifactToolNameFromCliArgs(
  args: Record<string, unknown> | undefined,
): string | undefined {
  const command = readCommandString(args);
  if (!command) return undefined;

  const unwrapped = unwrapShellCommand(command);
  const cliMatch = unwrapped.match(/(?:^|\s|["'])(?:\S*\/)?netcatty-tool-cli(?:\.(?:cjs|cmd))?(?=["'\s]|$)([\s\S]*)$/);
  if (!cliMatch) return undefined;

  const afterCli = stripWrappingQuote(cliMatch[1] ?? '').replace(/^["']?\s*/, '');
  const commandKey = afterCli
    .split(/\s+/)
    .filter((part) => part && !part.startsWith('-'))
    .slice(0, 3)
    .join(' ');

  return CLI_ARTIFACT_TOOL_NAMES.get(commandKey);
}
