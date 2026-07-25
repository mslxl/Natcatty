import { useCallback, useEffect, useMemo, useState } from "react";
import type { AuthenticationChallenge } from "@netcatty/plugin-contract";
import { isSafePluginAuthenticationUrl } from "../../domain/pluginConnection";
import { pluginExtensionBridge } from "./pluginExtensionBridge";

type ChallengeEvent = NetcattyPluginAuthenticationChallengeEvent;
export type PluginAuthenticationChallengeResponse = string | boolean | ReadonlyArray<string>;
type ActiveChallengeEvent = Extract<ChallengeEvent, { challenge: AuthenticationChallenge }>;

export const pluginAuthenticationChallengeMessage = (challenge: AuthenticationChallenge): string | undefined => (
  "message" in challenge && typeof challenge.message === "string" ? challenge.message : undefined
);

export function usePluginAuthenticationChallenges() {
  const [queue, setQueue] = useState<ActiveChallengeEvent[]>([]);
  const [textValue, setTextValue] = useState("");
  const [selectedChoices, setSelectedChoices] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const current = queue[0];
  const challenge = current?.challenge;

  useEffect(() => {
    return pluginExtensionBridge.onAuthenticationChallenge((event) => {
      setQueue((existing) => {
        if ("cancelled" in event && event.cancelled === true) {
          return existing.filter((item) => item.challengeRequestId !== event.challengeRequestId);
        }
        if (existing.some((item) => item.challengeRequestId === event.challengeRequestId)) return existing;
        if (existing.length >= 32) {
          void pluginExtensionBridge.respondAuthenticationChallenge({
            requestId: event.requestId,
            challengeRequestId: event.challengeRequestId,
            challengeId: event.challenge.id,
            cancelled: true,
          }).catch(() => {});
          return existing;
        }
        return [...existing, event].slice(0, 32);
      });
    });
  }, []);

  useEffect(() => {
    setTextValue("");
    setSelectedChoices([]);
    setBusy(false);
  }, [current?.challengeRequestId]);

  const complete = useCallback(async (
    response?: PluginAuthenticationChallengeResponse,
    cancelled = false,
  ) => {
    if (!current || busy) return;
    setBusy(true);
    try {
      await pluginExtensionBridge.respondAuthenticationChallenge({
        requestId: current.requestId,
        challengeRequestId: current.challengeRequestId,
        challengeId: current.challenge.id,
        ...(cancelled ? { cancelled: true } : { response }),
      });
      setQueue((existing) => existing.filter((item) => item.challengeRequestId !== current.challengeRequestId));
    } catch {
      setQueue((existing) => existing.filter((item) => item.challengeRequestId !== current.challengeRequestId));
    } finally {
      setBusy(false);
    }
  }, [busy, current]);

  const externalUrl = useMemo(() => {
    if (!challenge) return null;
    const value = challenge.kind === "browser"
      ? challenge.url
      : challenge.kind === "deviceCode"
        ? challenge.verificationUri
        : null;
    return value && isSafePluginAuthenticationUrl(value) ? value : null;
  }, [challenge]);

  const openExternal = useCallback(async () => {
    if (!externalUrl) return;
    await pluginExtensionBridge.openExternal(externalUrl);
  }, [externalUrl]);

  const isText = challenge?.kind === "text" || challenge?.kind === "password" || challenge?.kind === "otp";
  const canSubmit = challenge?.kind === "choice"
    ? selectedChoices.length > 0
    : isText
      ? textValue.length > 0
      : challenge?.kind === "browser" || challenge?.kind === "deviceCode"
        ? externalUrl !== null
        : Boolean(challenge);

  const submit = useCallback(() => {
    if (!challenge) return;
    if (isText) void complete(textValue);
    else if (challenge.kind === "choice") {
      void complete(challenge.multiple ? selectedChoices : selectedChoices[0]);
    } else {
      void complete(true);
    }
  }, [challenge, complete, isText, selectedChoices, textValue]);

  const setChoiceSelected = useCallback((choiceId: string, checked: boolean) => {
    setSelectedChoices((existing) => challenge?.kind === "choice" && challenge.multiple
      ? checked
        ? [...new Set([...existing, choiceId])]
        : existing.filter((id) => id !== choiceId)
      : checked ? [choiceId] : []);
  }, [challenge]);

  return {
    challenge,
    busy,
    textValue,
    setTextValue,
    selectedChoices,
    setChoiceSelected,
    externalUrl,
    openExternal,
    complete,
    isText,
    canSubmit,
    submit,
  };
}
