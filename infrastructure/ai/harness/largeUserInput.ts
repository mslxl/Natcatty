import type { ToolOutputStore } from './toolOutputStore';

const LARGE_USER_INPUT_THRESHOLD_CHARS = 25_000;
const LARGE_USER_INPUT_HEAD_CHARS = 12_000;
const LARGE_USER_INPUT_TAIL_CHARS = 4_000;

export function fitLargeUserInputForModel(
  input: string,
  chatSessionId: string,
  toolOutputStore: ToolOutputStore,
): string {
  if (input.length <= LARGE_USER_INPUT_THRESHOLD_CHARS) return input;
  const handle = toolOutputStore.store({
    chatSessionId,
    capabilityId: 'user.input',
    content: input,
  });
  return [
    input.slice(0, LARGE_USER_INPUT_HEAD_CHARS),
    `\n\n[... large user input moved to saved output: ${input.length} chars, handleId=${handle.id}. Use tool_output_read with range or search for omitted details ...]\n\n`,
    input.slice(-LARGE_USER_INPUT_TAIL_CHARS),
  ].join('');
}
