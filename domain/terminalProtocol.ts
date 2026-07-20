import type { Host, HostProtocol } from './models';

type TerminalTransportHost = Pick<Host, 'protocol' | 'moshEnabled' | 'etEnabled'>;

/** Resolve the transport actually selected by the first-party session launcher. */
export function resolveEffectiveTerminalProtocol(host: TerminalTransportHost): HostProtocol {
  if (host.protocol && host.protocol !== 'ssh') return host.protocol;
  if (host.etEnabled) return 'et';
  if (host.moshEnabled) return 'mosh';
  return host.protocol ?? 'ssh';
}
