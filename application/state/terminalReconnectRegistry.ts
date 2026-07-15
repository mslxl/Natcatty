type TerminalReconnectHandler = () => void;

export const createTerminalReconnectRegistry = () => {
  const handlers = new Map<string, TerminalReconnectHandler>();

  const register = (sessionId: string, handler: TerminalReconnectHandler): (() => void) => {
    handlers.set(sessionId, handler);
    return () => {
      if (handlers.get(sessionId) === handler) {
        handlers.delete(sessionId);
      }
    };
  };

  const request = (sessionId: string): boolean => {
    const handler = handlers.get(sessionId);
    if (!handler) return false;
    handler();
    return true;
  };

  return { register, request };
};

export const terminalReconnectRegistry = createTerminalReconnectRegistry();
