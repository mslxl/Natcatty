const containsCjkText = (text: string): boolean => /\p{Script=Han}/u.test(text);

export const selectMobaXtermDecodedText = ({
  utf8,
  fallback,
}: {
  utf8: string;
  fallback: string;
}): string => {
  if (containsCjkText(utf8)) return utf8;
  if (containsCjkText(fallback)) return fallback;
  return utf8;
};
