/** POSIX single-quote escaping for remote shell commands built in the renderer. */
export function shQuote(str: string): string {
  return `'${String(str).replace(/'/g, "'\"'\"'")}'`;
}

export function buildTmuxAttachCommand(sessionName: string, windowIndex?: number): string {
  const target = windowIndex !== undefined
    ? `${shQuote(sessionName)}:${windowIndex}`
    : shQuote(sessionName);
  return `tmux attach -t ${target}`;
}
