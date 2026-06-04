import type { ExternalAgentConfig } from "../../../../infrastructure/ai/types";
import {
  type ManagedAgentKey,
} from "../../../../infrastructure/ai/managedAgents";
import type { AgentPathInfo } from "./types";
import { AGENT_DEFAULTS } from "./types";

function isPathLikeCommand(command: string | undefined): boolean {
  const normalized = String(command || "").trim();
  return normalized.includes("/") || normalized.includes("\\");
}

function getAutoManagedAgentStoredPath(
  agents: ExternalAgentConfig[],
  agentKey: ManagedAgentKey,
): string | null {
  const managed = agents.find((agent) => agent.id === `discovered_${agentKey}`);
  return isPathLikeCommand(managed?.command) ? managed?.command ?? null : null;
}

export function areExternalAgentListsEqual(
  left: ExternalAgentConfig[],
  right: ExternalAgentConfig[],
): boolean {
  if (left.length !== right.length) return false;
  return left.every((agent, index) => JSON.stringify(agent) === JSON.stringify(right[index]));
}

export function buildManagedAgentState(
  prevAgents: ExternalAgentConfig[],
  defaultAgentId: string,
  agentKey: ManagedAgentKey,
  pathInfo: AgentPathInfo | null,
): { agents: ExternalAgentConfig[]; defaultAgentId: string } {
  const managedId = `discovered_${agentKey}`;
  const managedAgents = prevAgents.filter((agent) => agent.id === managedId);
  const otherAgents = prevAgents.filter((agent) => agent.id !== managedId);

  if (!pathInfo?.available || !pathInfo.path) {
    // If the user has pre-configured env vars (e.g. CODEBUDDY_AUTH_TOKEN)
    // before the CLI is installed, preserve the entry as disabled so their
    // settings survive until the CLI becomes available. Without this, a
    // failed "Check" or a temporarily-missing PATH would silently wipe
    // the user's configuration.
    const existingManaged = managedAgents.find((agent) => agent.id === managedId);
    const hasUserEnvConfig = Boolean(existingManaged?.env && Object.keys(existingManaged.env).length > 0);
    if (hasUserEnvConfig && existingManaged) {
      return {
        agents: [...otherAgents, { ...existingManaged, enabled: false, autoDisabledUntilAvailable: true }],
        defaultAgentId: managedAgents.some((agent) => agent.id === defaultAgentId)
          ? "catty"
          : defaultAgentId,
      };
    }
    return {
      agents: otherAgents,
      defaultAgentId: managedAgents.some((agent) => agent.id === defaultAgentId)
        ? "catty"
        : defaultAgentId,
    };
  }

  const existingManaged = managedAgents.find((agent) => agent.id === managedId);
  const { autoDisabledUntilAvailable: _autoDisabledUntilAvailable, ...existingManagedBase } = existingManaged ?? {};
  const defaults = AGENT_DEFAULTS[agentKey];
  const managedEnv = agentKey === "claude"
    ? { ...(existingManaged?.env ?? {}), CLAUDE_CODE_EXECUTABLE: pathInfo.path }
    : existingManaged?.env;
  // When the ACP command is the same binary as the agent CLI (e.g. codebuddy,
  // copilot), use the resolved path so custom installations not on PATH still work.
  // Agents with a separate ACP binary (e.g. codex-acp, claude-agent-acp) keep their
  // literal acpCommand unchanged.
  const resolvedAcpCommand = defaults.acpCommand === agentKey
    ? pathInfo.path
    : defaults.acpCommand;
  const hasExistingEnvConfig = Boolean(existingManaged?.env && Object.keys(existingManaged.env).length > 0);
  const isPreconfiguredUndetectedAgent = Boolean(
    hasExistingEnvConfig
    && existingManaged
    && !isPathLikeCommand(existingManaged.command),
  );
  const shouldEnableManagedAgent = managedAgents.length === 0
    || existingManaged?.autoDisabledUntilAvailable === true
    || isPreconfiguredUndetectedAgent
    || managedAgents.some((agent) => agent.enabled);
  const nextManagedAgent: ExternalAgentConfig = {
    ...existingManagedBase,
    ...defaults,
    id: managedId,
    command: pathInfo.path,
    acpCommand: resolvedAcpCommand,
    ...(managedEnv ? { env: managedEnv } : {}),
    enabled: shouldEnableManagedAgent,
  };

  return {
    agents: [...otherAgents, nextManagedAgent],
    defaultAgentId: managedAgents.some((agent) => agent.id === defaultAgentId)
      ? managedId
      : defaultAgentId,
  };
}

export function getInitialManagedAgentPaths(agents: ExternalAgentConfig[]) {
  return {
    codex: getAutoManagedAgentStoredPath(agents, "codex") ?? "",
    claude: getAutoManagedAgentStoredPath(agents, "claude") ?? "",
    copilot: getAutoManagedAgentStoredPath(agents, "copilot") ?? "",
    codebuddy: getAutoManagedAgentStoredPath(agents, "codebuddy") ?? "",
  };
}
