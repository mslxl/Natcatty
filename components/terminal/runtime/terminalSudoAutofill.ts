const ESCAPE_SEQUENCE = "\\x" + "1b";
const BELL_SEQUENCE = "\\x" + "07";
const BRACKETED_PASTE_START = "\x1b[200~";
const BRACKETED_PASTE_END = "\x1b[201~";
const ANSI_PATTERN = new RegExp(`${ESCAPE_SEQUENCE}\\[[0-?]*[ -/]*[@-~]`, "g");
const OSC_PATTERN = new RegExp(
  `${ESCAPE_SEQUENCE}\\][^${BELL_SEQUENCE}]*(?:${BELL_SEQUENCE}|${ESCAPE_SEQUENCE}\\\\)`,
  "g",
);
const SUDO_PROMPT_PATTERN =
  /(?:^|[\r\n])\s*(?:\[[^\]\r\n]*sudo[^\]\r\n]*\]\s*)?(?=.*\bsudo\b)(?:.*\bpassword\b(?:\s+for\s+[^:\r\n]+)?|.*密码(?:\s*[:：前为给]\s*[^:\r\n]*)?)\s*[:：]\s*$/i;
const SUDO_COMMAND_PATTERN = /^\s*(?:builtin\s+|command\s+)?sudo(?:\s|$)/;
const SUDO_COMMAND_HEAD_PATTERN = /^(\s*(?:builtin\s+|command\s+)?sudo)(?=\s|$)(.*)$/;
const SUDO_SHORT_OPTIONS_WITH_ARGUMENT = new Set(["C", "D", "g", "h", "p", "T", "t", "U", "u"]);
const SUDO_LONG_OPTIONS_WITH_ARGUMENT = new Set([
  "chdir",
  "close-from",
  "command-timeout",
  "group",
  "host",
  "prompt",
  "role",
  "type",
  "user",
]);

export const stripTerminalControlSequences = (data: string): string =>
  data.replace(OSC_PATTERN, "").replace(ANSI_PATTERN, "");

export const hasTerminalControlSequences = (data: string): boolean =>
  data.includes("\x1b") || data.includes("\x07");

export const isSudoPasswordPrompt = (
  data: string,
  expectedPrompt?: string,
): boolean => {
  if (expectedPrompt) {
    if (!data.includes(expectedPrompt)) return false;
    return isSudoPasswordPrompt(data.replaceAll(expectedPrompt, ""));
  }
  if (hasTerminalControlSequences(data)) return false;
  const text = stripTerminalControlSequences(data);
  return SUDO_PROMPT_PATTERN.test(text);
};

export const shouldArmSudoPasswordAutofill = (command: string): boolean =>
  SUDO_COMMAND_PATTERN.test(command);

export type SudoPasswordAutofill = {
  prepareCommand: (command: string) => string | null;
  handleOutput: (data: string) => string;
  updatePassword: (password?: string) => void;
};

export const prepareSudoAutofillInput = (
  data: string,
  recordedCommand: string | null,
  sudoAutofill: SudoPasswordAutofill | null | undefined,
): string => {
  if (data !== "\r" && data !== "\n") {
    if (data.startsWith(BRACKETED_PASTE_START) && data.endsWith(BRACKETED_PASTE_END)) {
      return data;
    }
    const pastedCommand = getSinglePastedCommand(data);
    if (!pastedCommand) return data;
    const preparedCommand = sudoAutofill?.prepareCommand(pastedCommand.command);
    if (!preparedCommand) return data;
    return `\x15${preparedCommand}${pastedCommand.lineEnding}`;
  }
  if (recordedCommand) {
    const preparedCommand = sudoAutofill?.prepareCommand(recordedCommand);
    if (preparedCommand) return `\x15${preparedCommand}${data}`;
  }
  return data;
};

export const getSinglePastedCommand = (
  data: string,
): { command: string; lineEnding: string } | null => {
  const match = unwrapBracketedPaste(data).match(/^([^\r\n]+)(\r\n|\r|\n)$/);
  if (!match) return null;
  return {
    command: match[1],
    lineEnding: match[2],
  };
};

export const getSingleBracketedPasteLine = (data: string): string | null => {
  if (!data.startsWith(BRACKETED_PASTE_START) || !data.endsWith(BRACKETED_PASTE_END)) {
    return null;
  }
  const text = unwrapBracketedPaste(data);
  if (!text || /[\r\n]/.test(text)) return null;
  return text;
};

const unwrapBracketedPaste = (data: string): string => {
  if (data.startsWith(BRACKETED_PASTE_START) && data.endsWith(BRACKETED_PASTE_END)) {
    return data.slice(BRACKETED_PASTE_START.length, -BRACKETED_PASTE_END.length);
  }
  return data;
};

const shellQuote = (value: string): string =>
  `'${value.replaceAll("'", "'\\''")}'`;

const createDefaultSudoPromptMarker = (): string =>
  `__NETCATTY_SUDO_${Math.random().toString(36).slice(2, 14)}__`;

const splitShellWords = (input: string): string[] => {
  const words: string[] = [];
  let current = "";
  let quote: "'" | '"' | null = null;
  let escaped = false;

  for (const char of input) {
    if (escaped) {
      current += char;
      escaped = false;
      continue;
    }
    if (char === "\\" && quote !== "'") {
      escaped = true;
      continue;
    }
    if ((char === "'" || char === "\"") && !quote) {
      quote = char;
      continue;
    }
    if (quote === char) {
      quote = null;
      continue;
    }
    if (!quote && /\s/.test(char)) {
      if (current) {
        words.push(current);
        current = "";
      }
      continue;
    }
    current += char;
  }

  if (current) words.push(current);
  return words;
};

const hasSudoPromptOption = (command: string): boolean => {
  const match = command.match(SUDO_COMMAND_HEAD_PATTERN);
  if (!match) return false;
  const tokens = splitShellWords(match[2]);
  for (let index = 0; index < tokens.length; index += 1) {
    const token = tokens[index];
    if (token === "--") return false;
    if (!token.startsWith("-") || token === "-") return false;

    if (token === "--prompt" || token.startsWith("--prompt=")) return true;
    if (token.startsWith("--")) {
      const optionName = token.slice(2).split("=")[0];
      if (SUDO_LONG_OPTIONS_WITH_ARGUMENT.has(optionName) && !token.includes("=")) {
        index += 1;
      }
      continue;
    }

    const shortOptions = token.slice(1);
    for (let optionIndex = 0; optionIndex < shortOptions.length; optionIndex += 1) {
      const option = shortOptions[optionIndex];
      if (option === "p") return true;
      if (SUDO_SHORT_OPTIONS_WITH_ARGUMENT.has(option)) {
        if (optionIndex === shortOptions.length - 1 && index + 1 < tokens.length) {
          index += 1;
        }
        break;
      }
    }
  }
  return false;
};

const buildSudoCommandWithPrompt = (
  command: string,
  prompt: string,
): string | null => {
  if (hasSudoPromptOption(command)) return null;
  const match = command.match(SUDO_COMMAND_HEAD_PATTERN);
  if (!match) return null;
  return `${match[1]} -p ${shellQuote(prompt)}${match[2]}`;
};

const isPotentialPreparedCommandPrefix = (
  data: string,
  preparedCommand?: string | null,
): boolean =>
  Boolean(preparedCommand && data && (
    preparedCommand.startsWith(data) || data.startsWith(preparedCommand)
  ));

const hasExpectedMarkerPrefix = (data: string, marker?: string | null): boolean => {
  if (!data || !marker) return false;
  const maxPrefixLength = Math.min(data.length, marker.length - 1);
  for (let length = maxPrefixLength; length > 0; length -= 1) {
    if (data.endsWith(marker.slice(0, length))) return true;
  }
  return false;
};

export const createSudoPasswordAutofill = (_options: {
  password?: string;
  write: (data: string) => void;
  now?: () => number;
  createPromptMarker?: () => string;
}): SudoPasswordAutofill => {
  const options = {
    now: () => Date.now(),
    createPromptMarker: createDefaultSudoPromptMarker,
    ..._options,
  };
  let password = options.password ?? "";
  const cooldownMs = 3_000;
  const armWindowMs = 30_000;
  let tail = "";
  let expectedPromptMarker: string | null = null;
  let originalCommand: string | null = null;
  let preparedCommand: string | null = null;
  let outputSanitizePending = "";
  let lastSentAt = Number.NEGATIVE_INFINITY;
  let armedUntil = Number.NEGATIVE_INFINITY;
  const disarm = () => {
    armedUntil = Number.NEGATIVE_INFINITY;
    tail = "";
    outputSanitizePending = "";
  };
  const applyOutputSanitizers = (data: string): string => {
    let nextData = data;
    if (preparedCommand && originalCommand) {
      nextData = nextData.replaceAll(preparedCommand, originalCommand);
    }
    if (expectedPromptMarker) {
      nextData = nextData.replaceAll(expectedPromptMarker, "");
    }
    return nextData;
  };
  const sanitizeOutput = (data: string, opts: { final?: boolean } = {}): string => {
    if (!preparedCommand && !expectedPromptMarker && !outputSanitizePending) return data;

    outputSanitizePending += data;
    const lastLineBreakIndex = Math.max(
      outputSanitizePending.lastIndexOf("\n"),
      outputSanitizePending.lastIndexOf("\r"),
    );
    if (!opts.final && lastLineBreakIndex < 0) {
      return "";
    }

    const readyLength = opts.final
      ? outputSanitizePending.length
      : lastLineBreakIndex + 1;
    const readyData = outputSanitizePending.slice(0, readyLength);
    outputSanitizePending = outputSanitizePending.slice(readyLength);
    return applyOutputSanitizers(readyData);
  };

  return {
    prepareCommand: (command: string) => {
      if (!password || !shouldArmSudoPasswordAutofill(command)) return null;
      const promptMarker = options.createPromptMarker();
      const prompt = `[sudo] password for %p: ${promptMarker}`;
      const nextPreparedCommand = buildSudoCommandWithPrompt(command, prompt);
      if (!nextPreparedCommand) return null;
      expectedPromptMarker = promptMarker;
      originalCommand = command;
      preparedCommand = nextPreparedCommand;
      armedUntil = options.now() + armWindowMs;
      tail = "";
      return nextPreparedCommand;
    },
    handleOutput: (data: string) => {
      const sanitizedData = sanitizeOutput(data);
      if (!password || armedUntil === Number.NEGATIVE_INFINITY) {
        if (
          !sanitizedData &&
          outputSanitizePending &&
          !isPotentialPreparedCommandPrefix(outputSanitizePending, preparedCommand) &&
          !hasExpectedMarkerPrefix(outputSanitizePending, expectedPromptMarker)
        ) {
          return sanitizeOutput("", { final: true });
        }
        return sanitizedData;
      }
      tail = `${tail}${data}`.slice(-1024);
      const currentTime = options.now();
      if (currentTime > armedUntil) {
        const finalSanitizedData = sanitizedData + sanitizeOutput("", { final: true });
        disarm();
        return finalSanitizedData;
      }
      if (currentTime - lastSentAt < cooldownMs) return sanitizedData;
      const lastLine = tail.split(/[\r\n]/).pop() ?? tail;
      if (!expectedPromptMarker || !isSudoPasswordPrompt(lastLine, expectedPromptMarker)) {
        if (
          lastLine &&
          !isPotentialPreparedCommandPrefix(lastLine, preparedCommand) &&
          !hasExpectedMarkerPrefix(lastLine, expectedPromptMarker)
        ) {
          return sanitizedData + sanitizeOutput("", { final: true });
        }
        return sanitizedData;
      }

      options.write(`${password}\n`);
      lastSentAt = currentTime;
      const finalSanitizedData = sanitizedData + sanitizeOutput("", { final: true });
      disarm();
      return finalSanitizedData;
    },
    updatePassword: (nextPassword?: string) => {
      password = nextPassword ?? "";
      if (!password) disarm();
    },
  };
};
