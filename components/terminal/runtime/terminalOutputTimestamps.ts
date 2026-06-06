export type TerminalOutputTimestampPrefixer = {
  append: (data: string) => string;
  reset: () => void;
};

type TerminalOutputTimestampPrefixerOptions = {
  now?: () => Date;
};

const pad2 = (value: number): string => value.toString().padStart(2, "0");

export const formatTerminalOutputTimestamp = (date: Date): string => (
  `\x1b[2;90m[${pad2(date.getHours())}:${pad2(date.getMinutes())}:${pad2(date.getSeconds())}] \x1b[22;39m`
);

const isCsiFinalByte = (char: string): boolean => char >= "@" && char <= "~";

const readEscapeSequence = (
  data: string,
  startIndex: number,
): { sequence: string; endIndex: number; isColorSequence: boolean } | null => {
  if (data[startIndex] !== "\x1b") return null;
  const next = data[startIndex + 1];
  if (!next) {
    return { sequence: "\x1b", endIndex: startIndex, isColorSequence: false };
  }

  if (next === "[") {
    for (let index = startIndex + 2; index < data.length; index += 1) {
      if (isCsiFinalByte(data[index])) {
        return {
          sequence: data.slice(startIndex, index + 1),
          endIndex: index,
          isColorSequence: data[index] === "m",
        };
      }
    }
    return {
      sequence: data.slice(startIndex),
      endIndex: data.length - 1,
      isColorSequence: false,
    };
  }

  if (next === "]") {
    for (let index = startIndex + 2; index < data.length; index += 1) {
      if (data[index] === "\u0007") {
        return {
          sequence: data.slice(startIndex, index + 1),
          endIndex: index,
          isColorSequence: false,
        };
      }
      if (data[index] === "\x1b" && data[index + 1] === "\\") {
        return {
          sequence: data.slice(startIndex, index + 2),
          endIndex: index + 1,
          isColorSequence: false,
        };
      }
    }
    return {
      sequence: data.slice(startIndex),
      endIndex: data.length - 1,
      isColorSequence: false,
    };
  }

  return {
    sequence: data.slice(startIndex, startIndex + 2),
    endIndex: startIndex + 1,
    isColorSequence: false,
  };
};

export const createTerminalOutputTimestampPrefixer = (
  options: TerminalOutputTimestampPrefixerOptions = {},
): TerminalOutputTimestampPrefixer => {
  const now = options.now ?? (() => new Date());
  let atLineStart = true;
  let currentLinePrefixed = false;

  const prefixIfNeeded = () => {
    if (!atLineStart || currentLinePrefixed) return "";
    currentLinePrefixed = true;
    atLineStart = false;
    return formatTerminalOutputTimestamp(now());
  };

  return {
    append(data: string) {
      let output = "";

      for (let index = 0; index < data.length; index += 1) {
        const char = data[index];

        if (char === "\x1b") {
          const sequence = readEscapeSequence(data, index);
          if (sequence) {
            if (sequence.isColorSequence) {
              output += prefixIfNeeded();
            }
            output += sequence.sequence;
            index = sequence.endIndex;
            continue;
          }
        }

        if (char !== "\r" && char !== "\n") {
          output += prefixIfNeeded();
        }
        output += char;

        if (char === "\n") {
          atLineStart = true;
          currentLinePrefixed = false;
        } else if (char === "\r") {
          atLineStart = true;
        } else if (char !== "\r") {
          atLineStart = false;
        }
      }

      return output;
    },
    reset() {
      atLineStart = true;
      currentLinePrefixed = false;
    },
  };
};
