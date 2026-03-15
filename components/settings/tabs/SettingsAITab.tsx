/**
 * Settings AI Tab - AI provider configuration, agent CLI detection, and safety settings
 */
import {
  Bot,
  Check,
  ChevronDown,
  Eye,
  EyeOff,
  ExternalLink,
  Globe,
  LogIn,
  LogOut,
  Pencil,
  Plus,
  RefreshCw,
  Shield,
  Trash2,
  X,
} from "lucide-react";
import React, { useCallback, useEffect, useMemo, useState } from "react";
import type {
  AIPermissionMode,
  AIProviderId,
  ExternalAgentConfig,
  ProviderConfig,
} from "../../../infrastructure/ai/types";
import { PROVIDER_PRESETS, DEFAULT_COMMAND_BLOCKLIST } from "../../../infrastructure/ai/types";
import { useAgentDiscovery } from "../../../application/state/useAgentDiscovery";
import { encryptField, decryptField } from "../../../infrastructure/persistence/secureFieldAdapter";
import { useI18n } from "../../../application/i18n/I18nProvider";
import { TabsContent } from "../../ui/tabs";
import { Button } from "../../ui/button";
import { Toggle, Select, SettingRow } from "../settings-ui";
import { cn } from "../../../lib/utils";
import { AgentIconBadge } from "../../ai/AgentIconBadge";

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

interface SettingsAITabProps {
  providers: ProviderConfig[];
  addProvider: (provider: ProviderConfig) => void;
  updateProvider: (id: string, updates: Partial<ProviderConfig>) => void;
  removeProvider: (id: string) => void;
  activeProviderId: string;
  setActiveProviderId: (id: string) => void;
  activeModelId: string;
  setActiveModelId: (id: string) => void;
  globalPermissionMode: AIPermissionMode;
  setGlobalPermissionMode: (mode: AIPermissionMode) => void;
  externalAgents: ExternalAgentConfig[];
  setExternalAgents: (value: ExternalAgentConfig[] | ((prev: ExternalAgentConfig[]) => ExternalAgentConfig[])) => void;
  defaultAgentId: string;
  setDefaultAgentId: (id: string) => void;
  commandBlocklist: string[];
  setCommandBlocklist: (value: string[]) => void;
  commandTimeout: number;
  setCommandTimeout: (value: number) => void;
  maxIterations: number;
  setMaxIterations: (value: number) => void;
}

type CodexIntegrationState =
  | "connected_chatgpt"
  | "connected_api_key"
  | "not_logged_in"
  | "unknown";

interface CodexIntegrationStatus {
  state: CodexIntegrationState;
  isConnected: boolean;
  rawOutput: string;
  exitCode: number | null;
}

type CodexLoginState = "running" | "success" | "error" | "cancelled";

interface CodexLoginSession {
  sessionId: string;
  state: CodexLoginState;
  url: string | null;
  output: string;
  error: string | null;
  exitCode: number | null;
}

interface AgentPathInfo {
  path: string | null;
  version: string | null;
  available: boolean;
}

interface NetcattyAiBridge {
  aiCodexGetIntegration?: () => Promise<CodexIntegrationStatus>;
  aiCodexStartLogin?: () => Promise<{ ok: boolean; session?: CodexLoginSession; error?: string }>;
  aiCodexGetLoginSession?: (sessionId: string) => Promise<{ ok: boolean; session?: CodexLoginSession; error?: string }>;
  aiCodexCancelLogin?: (sessionId: string) => Promise<{ ok: boolean; found?: boolean; session?: CodexLoginSession; error?: string }>;
  aiCodexLogout?: () => Promise<{ ok: boolean; state?: CodexIntegrationState; isConnected?: boolean; rawOutput?: string; logoutOutput?: string; error?: string }>;
  aiResolveCli?: (params: { command: string; customPath?: string }) => Promise<AgentPathInfo>;
  openExternal?: (url: string) => Promise<void>;
}

function getBridge(): NetcattyAiBridge | undefined {
  return (window as unknown as { netcatty?: NetcattyAiBridge }).netcatty;
}

function normalizeCodexBridgeError(error: unknown): string {
  const message = error instanceof Error ? error.message : String(error);
  if (message.includes("No handler registered for 'netcatty:ai:codex:")) {
    return "Codex main-process handlers are not loaded yet. Fully restart Netcatty, or restart the Electron dev process, then try again.";
  }
  return message;
}

// Agent default configs for registration in externalAgents
const AGENT_DEFAULTS: Record<string, Omit<ExternalAgentConfig, "id" | "command" | "enabled">> = {
  codex: {
    name: "Codex CLI",
    args: ["exec", "--full-auto", "--json", "{prompt}"],
    icon: "openai",
    acpCommand: "codex-acp",
    acpArgs: [],
  },
  claude: {
    name: "Claude Code",
    args: ["-p", "--output-format", "text", "{prompt}"],
    icon: "claude",
    acpCommand: "claude-code-acp",
    acpArgs: [],
  },
};

// ---------------------------------------------------------------------------
// Provider icon helper
// ---------------------------------------------------------------------------

type SettingsIconId = AIProviderId | "claude";

const SETTINGS_ICON_PATHS: Record<SettingsIconId, string> = {
  openai: "/ai/providers/openai.svg",
  anthropic: "/ai/providers/anthropic.svg",
  claude: "/ai/agents/claude.svg",
  google: "/ai/providers/google.svg",
  ollama: "/ai/providers/ollama.svg",
  openrouter: "/ai/providers/openrouter.svg",
  custom: "/ai/providers/custom.svg",
};

const SETTINGS_ICON_COLORS: Record<SettingsIconId, string> = {
  openai: "bg-emerald-600",
  anthropic: "bg-orange-600",
  claude: "bg-orange-600",
  google: "bg-blue-600",
  ollama: "bg-purple-600",
  openrouter: "bg-pink-600",
  custom: "bg-zinc-600",
};

const ProviderIconBadge: React.FC<{
  providerId: SettingsIconId;
  size?: "sm" | "md";
}> = ({ providerId, size = "md" }) => (
  <div
    className={cn(
      "rounded-md flex items-center justify-center shrink-0 overflow-hidden",
      size === "sm" ? "w-5 h-5" : "w-8 h-8",
      SETTINGS_ICON_COLORS[providerId],
    )}
  >
    <img
      src={SETTINGS_ICON_PATHS[providerId]}
      alt=""
      aria-hidden="true"
      draggable={false}
      className={cn(
        "object-contain brightness-0 invert",
        size === "sm" ? "w-3 h-3" : "w-4 h-4",
      )}
    />
  </div>
);

// ---------------------------------------------------------------------------
// Provider Config Form (inline expandable)
// ---------------------------------------------------------------------------

interface ProviderFormState {
  name: string;
  apiKey: string;
  baseURL: string;
  defaultModel: string;
}

// Fetch models from a provider's models endpoint (e.g. OpenRouter /api/v1/models)
interface FetchedModel {
  id: string;
  name?: string;
}

interface FetchBridge {
  aiFetch?: (url: string, method?: string, headers?: Record<string, string>, body?: string) => Promise<{ ok: boolean; data: string; error?: string }>;
}

function getFetchBridge(): FetchBridge | undefined {
  return (window as unknown as { netcatty?: FetchBridge }).netcatty;
}

// ---------------------------------------------------------------------------
// Model Selector (searchable dropdown with remote fetch)
// ---------------------------------------------------------------------------

const ModelSelector: React.FC<{
  value: string;
  onChange: (value: string) => void;
  baseURL: string;
  modelsEndpoint?: string;
  placeholder?: string;
  apiKey?: string;
  providerId?: AIProviderId;
}> = ({ value, onChange, baseURL, modelsEndpoint, placeholder, apiKey, providerId }) => {
  const { t } = useI18n();
  const [models, setModels] = useState<FetchedModel[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isOpen, setIsOpen] = useState(false);
  const [hasFetched, setHasFetched] = useState(false);

  // Ollama runs locally without auth; all other providers need an API key to list models
  const needsApiKey = providerId !== "ollama";
  const canFetch = !!modelsEndpoint && (!needsApiKey || !!apiKey);

  const fetchModels = useCallback(async () => {
    if (!modelsEndpoint) return;
    const bridge = getFetchBridge();
    if (!bridge?.aiFetch) return;

    setIsLoading(true);
    setError(null);
    try {
      const url = `${baseURL.replace(/\/+$/, "")}${modelsEndpoint}`;
      const headers: Record<string, string> = {};
      if (apiKey) {
        if (providerId === "anthropic") {
          headers["x-api-key"] = apiKey;
          headers["anthropic-version"] = "2023-06-01";
        } else {
          headers["Authorization"] = `Bearer ${apiKey}`;
        }
      }
      const result = await bridge.aiFetch(url, "GET", headers);
      if (!result.ok) {
        setError(`Failed to fetch models (${result.error || "unknown error"})`);
        return;
      }
      const parsed = JSON.parse(result.data);
      const list: FetchedModel[] = (parsed.data || parsed.models || []).map((m: { id: string; name?: string }) => ({
        id: m.id,
        name: m.name,
      }));
      list.sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id));
      setModels(list);
      setHasFetched(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to parse response");
    } finally {
      setIsLoading(false);
    }
  }, [baseURL, modelsEndpoint, apiKey, providerId]);

  // Auto-fetch when dropdown first opens
  useEffect(() => {
    if (isOpen && canFetch && !hasFetched && !isLoading) {
      void fetchModels();
    }
  }, [isOpen, canFetch, hasFetched, isLoading, fetchModels]);

  // Filter models by current input value (inline autocomplete)
  const suggestions = useMemo(() => {
    if (!hasFetched || models.length === 0) return [];
    if (!value.trim()) return models;
    const q = value.toLowerCase();
    return models.filter((m) =>
      m.id.toLowerCase().includes(q) || (m.name && m.name.toLowerCase().includes(q)),
    );
  }, [models, value, hasFetched]);

  const showSuggestions = isOpen && canFetch;

  return (
    <div className="relative">
      <div className="flex items-center gap-2">
        <div className="relative flex-1">
          <input
            type="text"
            value={value}
            onChange={(e) => {
              onChange(e.target.value);
              if (canFetch && hasFetched && !isOpen) setIsOpen(true);
            }}
            onFocus={() => { if (canFetch) setIsOpen(true); }}
            onBlur={() => { setTimeout(() => setIsOpen(false), 150); }}
            placeholder={placeholder ?? (canFetch ? t('ai.providers.searchModel') : t('ai.providers.defaultModel.placeholder'))}
            className={cn(
              "w-full h-8 rounded-md border border-input bg-background px-3 text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring",
              canFetch && "pr-8",
            )}
          />
          {canFetch && (
            <button
              type="button"
              onMouseDown={(e) => { e.preventDefault(); setIsOpen(!isOpen); }}
              className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
            >
              <ChevronDown size={14} className={cn("transition-transform", isOpen && "rotate-180")} />
            </button>
          )}
        </div>
        {canFetch && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => { setHasFetched(false); void fetchModels(); }}
            disabled={isLoading}
            className="shrink-0 px-2"
            title={t('ai.providers.refreshModels')}
          >
            <RefreshCw size={14} className={isLoading ? "animate-spin" : ""} />
          </Button>
        )}
      </div>

      {/* Suggestions dropdown */}
      {showSuggestions && (
        <div className="absolute top-full left-0 right-0 mt-1 z-[101] rounded-md border border-border bg-popover shadow-md">
          <div className="max-h-60 overflow-y-auto">
            {isLoading ? (
              <div className="px-3 py-3 text-center text-xs text-muted-foreground">
                <RefreshCw size={14} className="animate-spin inline mr-1.5" />
                {t('ai.providers.loadingModels')}
              </div>
            ) : error ? (
              <div className="px-3 py-3 text-center text-xs text-destructive">{error}</div>
            ) : suggestions.length === 0 ? (
              <div className="px-3 py-3 text-center text-xs text-muted-foreground">
                {hasFetched ? t('ai.providers.noMatchingModels') : t('ai.providers.clickToLoadModels')}
              </div>
            ) : (
              suggestions.slice(0, 100).map((m) => (
                <button
                  key={m.id}
                  onMouseDown={(e) => {
                    e.preventDefault();
                    onChange(m.id);
                    setIsOpen(false);
                  }}
                  className={cn(
                    "w-full text-left px-3 py-1.5 text-xs hover:bg-accent transition-colors flex items-center justify-between gap-2",
                    m.id === value && "bg-accent",
                  )}
                >
                  <span className="font-mono truncate">{m.id}</span>
                  {m.id === value && <Check size={12} className="text-primary shrink-0" />}
                </button>
              ))
            )}
            {suggestions.length > 100 && (
              <div className="px-3 py-2 text-center text-[10px] text-muted-foreground border-t border-border/40">
                {t('ai.providers.showingModels').replace('{count}', String(suggestions.length))}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
};

const ProviderConfigForm: React.FC<{
  provider: ProviderConfig;
  onSave: (updates: Partial<ProviderConfig>) => void;
  onCancel: () => void;
}> = ({ provider, onSave, onCancel }) => {
  const { t } = useI18n();
  const [form, setForm] = useState<ProviderFormState>({
    name: provider.name ?? PROVIDER_PRESETS[provider.providerId]?.name ?? "",
    apiKey: "",
    baseURL: provider.baseURL ?? PROVIDER_PRESETS[provider.providerId]?.defaultBaseURL ?? "",
    defaultModel: provider.defaultModel ?? "",
  });
  const isCustom = provider.providerId === "custom";
  const [showApiKey, setShowApiKey] = useState(false);
  const [isDecrypting, setIsDecrypting] = useState(false);

  const preset = PROVIDER_PRESETS[provider.providerId];

  // Decrypt and load existing API key on mount
  useEffect(() => {
    if (provider.apiKey) {
      setIsDecrypting(true);
      decryptField(provider.apiKey)
        .then((decrypted) => {
          setForm((prev) => ({ ...prev, apiKey: decrypted ?? "" }));
        })
        .catch(() => {
          // If decryption fails, show raw value
          setForm((prev) => ({ ...prev, apiKey: provider.apiKey ?? "" }));
        })
        .finally(() => setIsDecrypting(false));
    }
  }, [provider.apiKey]);

  const handleSave = useCallback(async () => {
    const updates: Partial<ProviderConfig> = {
      baseURL: form.baseURL || undefined,
      defaultModel: form.defaultModel || undefined,
      ...(isCustom && form.name.trim() ? { name: form.name.trim() } : {}),
    };

    // Encrypt API key before saving
    if (form.apiKey) {
      updates.apiKey = await encryptField(form.apiKey);
    } else {
      updates.apiKey = undefined;
    }

    onSave(updates);
  }, [form, onSave]);

  return (
    <div className="mt-3 space-y-3 border-t border-border/40 pt-3">
      {/* Name (custom providers only) */}
      {isCustom && (
        <div className="space-y-1.5">
          <label className="text-xs font-medium text-muted-foreground">{t('ai.providers.name')}</label>
          <input
            type="text"
            value={form.name}
            onChange={(e) => setForm((prev) => ({ ...prev, name: e.target.value }))}
            placeholder={t('ai.providers.name.placeholder')}
            className="w-full h-8 rounded-md border border-input bg-background px-3 text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
          />
        </div>
      )}
      {/* API Key */}
      <div className="space-y-1.5">
        <label className="text-xs font-medium text-muted-foreground">{t('ai.providers.apiKey')}</label>
        <div className="flex items-center gap-2">
          <div className="relative flex-1">
            <input
              type={showApiKey ? "text" : "password"}
              value={isDecrypting ? "" : form.apiKey}
              onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
              placeholder={isDecrypting ? t('ai.providers.apiKey.decrypting') : t('ai.providers.apiKey.placeholder')}
              disabled={isDecrypting}
              className="w-full h-8 rounded-md border border-input bg-background px-3 pr-9 text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:opacity-50"
            />
            <button
              type="button"
              onClick={() => setShowApiKey(!showApiKey)}
              className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
            >
              {showApiKey ? <EyeOff size={14} /> : <Eye size={14} />}
            </button>
          </div>
        </div>
      </div>

      {/* Base URL */}
      <div className="space-y-1.5">
        <label className="text-xs font-medium text-muted-foreground">{t('ai.providers.baseUrl')}</label>
        <input
          type="text"
          value={form.baseURL}
          onChange={(e) => setForm((prev) => ({ ...prev, baseURL: e.target.value }))}
          placeholder={preset?.defaultBaseURL || "https://"}
          className="w-full h-8 rounded-md border border-input bg-background px-3 text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        />
      </div>

      {/* Default Model */}
      <div className="space-y-1.5">
        <label className="text-xs font-medium text-muted-foreground">{t('ai.providers.defaultModel')}</label>
        <ModelSelector
          value={form.defaultModel}
          onChange={(val) => setForm((prev) => ({ ...prev, defaultModel: val }))}
          baseURL={form.baseURL || preset?.defaultBaseURL || ""}
          modelsEndpoint={preset?.modelsEndpoint}
          apiKey={form.apiKey}
          providerId={provider.providerId}
        />
      </div>

      {/* Actions */}
      <div className="flex items-center gap-2 pt-1">
        <Button variant="default" size="sm" onClick={() => void handleSave()}>
          <Check size={14} className="mr-1.5" />
          {t('common.save')}
        </Button>
        <Button variant="ghost" size="sm" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
      </div>
    </div>
  );
};

// ---------------------------------------------------------------------------
// Provider Card
// ---------------------------------------------------------------------------

const ProviderCard: React.FC<{
  provider: ProviderConfig;
  isActive: boolean;
  onToggleEnabled: (enabled: boolean) => void;
  onEdit: () => void;
  onRemove: () => void;
  onUpdate: (updates: Partial<ProviderConfig>) => void;
  isEditing: boolean;
  onCancelEdit: () => void;
}> = ({ provider, isActive, onToggleEnabled, onEdit, onRemove, onUpdate, isEditing, onCancelEdit }) => {
  const { t } = useI18n();
  const hasApiKey = !!provider.apiKey;

  return (
    <div
      className={cn(
        "rounded-lg border p-4 transition-colors",
        isActive ? "border-primary/50 bg-primary/5" : "border-border/60 bg-muted/20",
      )}
    >
      <div className="flex items-center gap-3">
        {/* Provider icon */}
        <ProviderIconBadge providerId={provider.providerId} />

        {/* Info */}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium truncate">{provider.name}</span>
            {isActive && (
              <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-primary/20 text-primary font-medium">
                {t('ai.providers.active')}
              </span>
            )}
          </div>
          <div className="flex items-center gap-2 mt-0.5">
            <span
              className={cn(
                "text-xs",
                hasApiKey ? "text-emerald-500" : "text-muted-foreground",
              )}
            >
              {hasApiKey ? t('ai.providers.apiKeyConfigured') : t('ai.providers.noApiKey')}
            </span>
            {provider.defaultModel && (
              <>
                <span className="text-muted-foreground text-xs">|</span>
                <span className="text-xs text-muted-foreground truncate">{provider.defaultModel}</span>
              </>
            )}
          </div>
        </div>

        {/* Actions */}
        <div className="flex items-center gap-1 shrink-0">
          <button
            onClick={onEdit}
            className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
            title={t('ai.providers.configure')}
          >
            <Pencil size={14} />
          </button>
          <button
            onClick={onRemove}
            className="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors"
            title={t('ai.providers.remove')}
          >
            <Trash2 size={14} />
          </button>
          <Toggle checked={provider.enabled} onChange={onToggleEnabled} />
        </div>
      </div>

      {/* Expandable config form */}
      {isEditing && (
        <ProviderConfigForm
          provider={provider}
          onSave={(updates) => {
            onUpdate(updates);
            onCancelEdit();
          }}
          onCancel={onCancelEdit}
        />
      )}
    </div>
  );
};

// ---------------------------------------------------------------------------
// Add Provider Dropdown
// ---------------------------------------------------------------------------

const AddProviderDropdown: React.FC<{
  onAdd: (providerId: AIProviderId) => void;
}> = ({ onAdd }) => {
  const { t } = useI18n();
  const [isOpen, setIsOpen] = useState(false);

  const providerIds = Object.keys(PROVIDER_PRESETS) as AIProviderId[];

  return (
    <div className="relative">
      <Button
        variant="outline"
        size="sm"
        onClick={() => setIsOpen(!isOpen)}
        className="gap-1.5"
      >
        <Plus size={14} />
        {t('ai.providers.add')}
        <ChevronDown size={12} className={cn("transition-transform", isOpen && "rotate-180")} />
      </Button>

      {isOpen && (
        <>
          {/* Backdrop */}
          <div className="fixed inset-0 z-[100]" onClick={() => setIsOpen(false)} />
          {/* Menu */}
          <div className="absolute top-full left-0 mt-1 z-[101] min-w-[200px] rounded-md border border-border bg-popover shadow-md py-1">
            {providerIds.map((pid) => (
              <button
                key={pid}
                onClick={() => {
                  onAdd(pid);
                  setIsOpen(false);
                }}
                className="w-full flex items-center gap-2.5 px-3 py-2 text-sm hover:bg-accent transition-colors text-left"
              >
                <ProviderIconBadge providerId={pid} size="sm" />
                {PROVIDER_PRESETS[pid].name}
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  );
};

// ---------------------------------------------------------------------------
// Codex Connection Card (with path detection)
// ---------------------------------------------------------------------------

const CodexConnectionCard: React.FC<{
  pathInfo: AgentPathInfo | null;
  isResolvingPath: boolean;
  customPath: string;
  onCustomPathChange: (path: string) => void;
  onRecheckPath: () => void;
  integration: CodexIntegrationStatus | null;
  loginSession: CodexLoginSession | null;
  isLoading: boolean;
  hasOpenAiProviderKey: boolean;
  error: string | null;
  onRefresh: () => void;
  onConnect: () => void;
  onCancel: () => void;
  onOpenUrl: () => void;
  onLogout: () => void;
}> = ({
  pathInfo,
  isResolvingPath,
  customPath,
  onCustomPathChange,
  onRecheckPath,
  integration,
  loginSession,
  isLoading,
  hasOpenAiProviderKey,
  error,
  onRefresh,
  onConnect,
  onCancel,
  onOpenUrl,
  onLogout,
}) => {
  const { t } = useI18n();
  const found = pathInfo?.available;

  const status = isResolvingPath
    ? t('ai.codex.detecting')
    : !found
      ? t('ai.codex.notFound')
      : loginSession?.state === "running"
        ? t('ai.codex.awaitingLogin')
        : integration?.state === "connected_chatgpt"
          ? t('ai.codex.connectedChatGPT')
          : integration?.state === "connected_api_key"
            ? t('ai.codex.connectedApiKey')
            : integration?.state === "not_logged_in"
              ? t('ai.codex.notConnected')
              : t('ai.codex.statusUnknown');

  const statusClassName = isResolvingPath
    ? "text-muted-foreground"
    : !found
      ? "text-amber-500"
      : loginSession?.state === "running"
        ? "text-amber-500"
        : integration?.isConnected
          ? "text-emerald-500"
          : "text-muted-foreground";

  const outputText = loginSession?.error
    ? loginSession.error
    : loginSession?.output?.trim()
      ? loginSession.output.trim()
      : integration?.rawOutput?.trim()
        ? integration.rawOutput.trim()
        : "";

  return (
    <div className="rounded-lg border border-border/60 bg-muted/20 p-4 space-y-3">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <ProviderIconBadge providerId="openai" size="sm" />
            <span className="text-sm font-medium">{t('ai.codex.title')}</span>
          </div>
          <p className="text-xs text-muted-foreground mt-2 leading-5">
            {t('ai.codex.description')}
          </p>
        </div>
        <div className={cn("text-xs font-medium shrink-0", statusClassName)}>
          {status}
        </div>
      </div>

      {/* Path detection info */}
      {found ? (
        <div className="flex items-center gap-2 text-xs">
          <span className="text-muted-foreground">{t('ai.codex.path')}</span>
          <span className="font-mono text-foreground truncate">{pathInfo.path}</span>
          {pathInfo.version && (
            <>
              <span className="text-muted-foreground">|</span>
              <span className="text-muted-foreground">{pathInfo.version}</span>
            </>
          )}
        </div>
      ) : !isResolvingPath ? (
        <div className="space-y-2">
          <p className="text-xs text-amber-500">
            {t('ai.codex.notFoundHint')}
          </p>
          <div className="flex items-center gap-2">
            <input
              type="text"
              value={customPath}
              onChange={(e) => onCustomPathChange(e.target.value)}
              placeholder={t('ai.codex.customPathPlaceholder')}
              className="flex-1 h-8 rounded-md border border-input bg-background px-3 text-sm font-mono placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
            />
            <Button variant="outline" size="sm" onClick={onRecheckPath} disabled={!customPath.trim()}>
              <RefreshCw size={14} className="mr-1.5" />
              {t('ai.codex.check')}
            </Button>
          </div>
        </div>
      ) : null}

      {/* Connection & login UI – only when codex is detected */}
      {found && (
        <>
          <div className="border-t border-border/40 pt-3 flex items-center gap-2 flex-wrap">
            {loginSession?.state === "running" ? (
              <>
                <Button variant="default" size="sm" onClick={onOpenUrl} disabled={!loginSession.url}>
                  <ExternalLink size={14} className="mr-1.5" />
                  {t('ai.codex.openLogin')}
                </Button>
                <Button variant="outline" size="sm" onClick={onCancel}>
                  <X size={14} className="mr-1.5" />
                  {t('common.cancel')}
                </Button>
              </>
            ) : integration?.isConnected ? (
              <Button variant="outline" size="sm" onClick={onLogout}>
                <LogOut size={14} className="mr-1.5" />
                {t('ai.codex.logout')}
              </Button>
            ) : (
              <Button variant="default" size="sm" onClick={onConnect}>
                <LogIn size={14} className="mr-1.5" />
                {t('ai.codex.connectChatGPT')}
              </Button>
            )}

            <Button variant="outline" size="sm" onClick={onRefresh} disabled={isLoading}>
              <RefreshCw size={14} className={cn("mr-1.5", isLoading && "animate-spin")} />
              {t('ai.codex.refreshStatus')}
            </Button>
          </div>

          {hasOpenAiProviderKey && (
            <p className="text-xs text-emerald-500">
              {t('ai.codex.apiKeyHint')}
            </p>
          )}
        </>
      )}

      {error && (
        <p className="text-xs text-destructive">
          {error}
        </p>
      )}

      {found && outputText && (
        <pre className="rounded-md border border-border/60 bg-background px-3 py-2 text-[11px] leading-5 text-muted-foreground whitespace-pre-wrap max-h-40 overflow-auto">
          {outputText}
        </pre>
      )}
    </div>
  );
};

// ---------------------------------------------------------------------------
// Claude Code Card (with path detection)
// ---------------------------------------------------------------------------

const ClaudeCodeCard: React.FC<{
  pathInfo: AgentPathInfo | null;
  isResolvingPath: boolean;
  customPath: string;
  onCustomPathChange: (path: string) => void;
  onRecheckPath: () => void;
}> = ({
  pathInfo,
  isResolvingPath,
  customPath,
  onCustomPathChange,
  onRecheckPath,
}) => {
  const { t } = useI18n();
  const found = pathInfo?.available;

  const statusText = isResolvingPath
    ? t('ai.claude.detecting')
    : found
      ? t('ai.claude.detected')
      : t('ai.claude.notFound');

  const statusClassName = isResolvingPath
    ? "text-muted-foreground"
    : found
      ? "text-emerald-500"
      : "text-amber-500";

  return (
    <div className="rounded-lg border border-border/60 bg-muted/20 p-4 space-y-3">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <ProviderIconBadge providerId="claude" size="sm" />
            <span className="text-sm font-medium">{t('ai.claude.title')}</span>
          </div>
          <p className="text-xs text-muted-foreground mt-2 leading-5">
            {t('ai.claude.description')}
          </p>
        </div>
        <div className={cn("text-xs font-medium shrink-0", statusClassName)}>
          {statusText}
        </div>
      </div>

      {/* Path detection info */}
      {found ? (
        <div className="flex items-center gap-2 text-xs">
          <span className="text-muted-foreground">{t('ai.claude.path')}</span>
          <span className="font-mono text-foreground truncate">{pathInfo.path}</span>
          {pathInfo.version && (
            <>
              <span className="text-muted-foreground">|</span>
              <span className="text-muted-foreground">{pathInfo.version}</span>
            </>
          )}
        </div>
      ) : !isResolvingPath ? (
        <div className="space-y-2">
          <p className="text-xs text-amber-500">
            {t('ai.claude.notFoundHint')}
          </p>
          <div className="flex items-center gap-2">
            <input
              type="text"
              value={customPath}
              onChange={(e) => onCustomPathChange(e.target.value)}
              placeholder={t('ai.claude.customPathPlaceholder')}
              className="flex-1 h-8 rounded-md border border-input bg-background px-3 text-sm font-mono placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
            />
            <Button variant="outline" size="sm" onClick={onRecheckPath} disabled={!customPath.trim()}>
              <RefreshCw size={14} className="mr-1.5" />
              {t('ai.claude.check')}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
};

// ---------------------------------------------------------------------------
// Main Tab Component
// ---------------------------------------------------------------------------

const SettingsAITab: React.FC<SettingsAITabProps> = ({
  providers,
  addProvider,
  updateProvider,
  removeProvider,
  activeProviderId,
  setActiveProviderId,
  activeModelId: _activeModelId,
  setActiveModelId,
  globalPermissionMode,
  setGlobalPermissionMode,
  externalAgents,
  setExternalAgents,
  defaultAgentId,
  setDefaultAgentId,
  commandBlocklist,
  setCommandBlocklist,
  commandTimeout,
  setCommandTimeout,
  maxIterations,
  setMaxIterations,
}) => {
  const { t } = useI18n();
  const [editingProviderId, setEditingProviderId] = useState<string | null>(null);
  const [codexIntegration, setCodexIntegration] = useState<CodexIntegrationStatus | null>(null);
  const [codexLoginSession, setCodexLoginSession] = useState<CodexLoginSession | null>(null);
  const [isCodexLoading, setIsCodexLoading] = useState(false);
  const [codexError, setCodexError] = useState<string | null>(null);

  // Path detection state
  const [codexPathInfo, setCodexPathInfo] = useState<AgentPathInfo | null>(null);
  const [codexCustomPath, setCodexCustomPath] = useState("");
  const [isResolvingCodex, setIsResolvingCodex] = useState(false);

  const [claudePathInfo, setClaudePathInfo] = useState<AgentPathInfo | null>(null);
  const [claudeCustomPath, setClaudeCustomPath] = useState("");
  const [isResolvingClaude, setIsResolvingClaude] = useState(false);

  const {
    discoveredAgents,
    isDiscovering,
    enableAgent,
  } = useAgentDiscovery(externalAgents, setExternalAgents);

  // Derive path info from discovery results
  useEffect(() => {
    if (isDiscovering) return;

    const codex = discoveredAgents.find((a) => a.command === "codex");
    setCodexPathInfo(
      codex
        ? { path: codex.path, version: codex.version, available: true }
        : { path: null, version: null, available: false },
    );

    const claude = discoveredAgents.find((a) => a.command === "claude");
    setClaudePathInfo(
      claude
        ? { path: claude.path, version: claude.version, available: true }
        : { path: null, version: null, available: false },
    );
  }, [isDiscovering, discoveredAgents]);

  // Auto-register discovered agents in externalAgents
  useEffect(() => {
    if (isDiscovering || discoveredAgents.length === 0) return;

    setExternalAgents((prev) => {
      const agentsToRegister: ExternalAgentConfig[] = [];

      for (const da of discoveredAgents) {
        if (da.command !== "codex" && da.command !== "claude") continue;
        const agentId = `discovered_${da.command}`;
        if (prev.some((ea) => ea.id === agentId)) continue;
        agentsToRegister.push(enableAgent(da));
      }

      return agentsToRegister.length > 0 ? [...prev, ...agentsToRegister] : prev;
    });
  }, [isDiscovering, discoveredAgents, enableAgent, setExternalAgents]);

  // Validate a custom path for an agent
  const handleCheckCustomPath = useCallback(async (agentKey: "codex" | "claude") => {
    const bridge = getBridge();
    if (!bridge?.aiResolveCli) return;

    const customPath = agentKey === "codex" ? codexCustomPath : claudeCustomPath;
    const setInfo = agentKey === "codex" ? setCodexPathInfo : setClaudePathInfo;
    const setResolving = agentKey === "codex" ? setIsResolvingCodex : setIsResolvingClaude;

    setResolving(true);
    try {
      const result = await bridge.aiResolveCli({
        command: agentKey,
        customPath: customPath.trim(),
      });
      setInfo(result);

      // Register/update in externalAgents if valid
      if (result.available && result.path) {
        const agentId = `discovered_${agentKey}`;
        const defaults = AGENT_DEFAULTS[agentKey];
        setExternalAgents((prev) => {
          const idx = prev.findIndex((a) => a.id === agentId);
          const config: ExternalAgentConfig = {
            id: agentId,
            command: result.path!,
            enabled: true,
            ...defaults,
          };
          if (idx >= 0) {
            const updated = [...prev];
            updated[idx] = { ...updated[idx], command: result.path! };
            return updated;
          }
          return [...prev, config];
        });
      }
    } catch (err) {
      console.error("Path resolution failed:", err);
    } finally {
      setResolving(false);
    }
  }, [codexCustomPath, claudeCustomPath, setExternalAgents]);

  // Add a new provider from preset
  const handleAddProvider = useCallback(
    (providerId: AIProviderId) => {
      const preset = PROVIDER_PRESETS[providerId];
      const id = `provider_${Date.now()}_${Math.random().toString(36).slice(2, 6)}`;
      addProvider({
        id,
        providerId,
        name: preset.name,
        baseURL: preset.defaultBaseURL,
        enabled: false,
      });
      // Auto-open config form
      setEditingProviderId(id);
    },
    [addProvider],
  );

  // Remove provider with confirmation
  const handleRemoveProvider = useCallback(
    (id: string) => {
      removeProvider(id);
      if (editingProviderId === id) {
        setEditingProviderId(null);
      }
    },
    [removeProvider, editingProviderId],
  );

  // Permission mode options
  const permissionModeOptions = [
    { value: "observer", label: t('ai.safety.permissionMode.observer') },
    { value: "confirm", label: t('ai.safety.permissionMode.confirm') },
    { value: "autonomous", label: t('ai.safety.permissionMode.autonomous') },
  ];

  // Agent options for default agent
  const agentOptions = useMemo(() => [
    { value: "catty", label: t('ai.defaultAgent.catty'), icon: <AgentIconBadge agent={{ id: "catty", type: "builtin" }} size="xs" variant="plain" /> },
    ...externalAgents
      .filter((a) => a.enabled)
      .map((a) => ({ value: a.id, label: a.name, icon: <AgentIconBadge agent={a} size="xs" variant="plain" /> })),
  ], [externalAgents]);

  const hasOpenAiProviderKey = providers.some(
    (provider) => provider.providerId === "openai" && provider.enabled && !!provider.apiKey,
  );

  const refreshCodexIntegration = useCallback(async () => {
    const bridge = getBridge();
    if (!bridge?.aiCodexGetIntegration) return;

    setIsCodexLoading(true);
    setCodexError(null);
    try {
      const integration = await bridge.aiCodexGetIntegration();
      setCodexIntegration(integration);
    } catch (err) {
      setCodexError(normalizeCodexBridgeError(err));
    } finally {
      setIsCodexLoading(false);
    }
  }, []);

  useEffect(() => {
    void refreshCodexIntegration();
  }, [refreshCodexIntegration]);

  useEffect(() => {
    if (!codexLoginSession || codexLoginSession.state !== "running") {
      return;
    }

    const bridge = getBridge();
    if (!bridge?.aiCodexGetLoginSession) {
      return;
    }

    let cancelled = false;
    const intervalId = window.setInterval(() => {
      void bridge.aiCodexGetLoginSession?.(codexLoginSession.sessionId).then((result) => {
        if (cancelled || !result?.ok || !result.session) return;

        setCodexLoginSession(result.session);
        if (result.session.state !== "running") {
          if (result.session.state === "success") {
            void refreshCodexIntegration();
          }
        }
      }).catch((err) => {
        if (!cancelled) {
          setCodexError(normalizeCodexBridgeError(err));
        }
      });
    }, 1000);

    return () => {
      cancelled = true;
      window.clearInterval(intervalId);
    };
  }, [codexLoginSession, refreshCodexIntegration]);

  const handleStartCodexLogin = useCallback(async () => {
    const bridge = getBridge();
    if (!bridge?.aiCodexStartLogin) return;

    setCodexError(null);
    setIsCodexLoading(true);
    try {
      const result = await bridge.aiCodexStartLogin();
      if (!result.ok || !result.session) {
        throw new Error(result.error || "Failed to start Codex login");
      }
      setCodexLoginSession(result.session);
    } catch (err) {
      setCodexError(normalizeCodexBridgeError(err));
    } finally {
      setIsCodexLoading(false);
    }
  }, []);

  const handleCancelCodexLogin = useCallback(async () => {
    const bridge = getBridge();
    if (!bridge?.aiCodexCancelLogin || !codexLoginSession) return;

    setCodexError(null);
    try {
      const result = await bridge.aiCodexCancelLogin(codexLoginSession.sessionId);
      if (result.session) {
        setCodexLoginSession(result.session);
      }
    } catch (err) {
      setCodexError(normalizeCodexBridgeError(err));
    }
  }, [codexLoginSession]);

  const handleOpenCodexLoginUrl = useCallback(() => {
    const bridge = getBridge();
    const url = codexLoginSession?.url;
    if (!bridge?.openExternal || !url) return;
    void bridge.openExternal(url);
  }, [codexLoginSession]);

  const handleCodexLogout = useCallback(async () => {
    const bridge = getBridge();
    if (!bridge?.aiCodexLogout) return;

    setCodexError(null);
    setIsCodexLoading(true);
    try {
      const result = await bridge.aiCodexLogout();
      if (!result.ok) {
        throw new Error(result.error || "Failed to log out from Codex");
      }
      setCodexLoginSession(null);
      await refreshCodexIntegration();
    } catch (err) {
      setCodexError(normalizeCodexBridgeError(err));
    } finally {
      setIsCodexLoading(false);
    }
  }, [refreshCodexIntegration]);

  return (
    <TabsContent
      value="ai"
      className="data-[state=inactive]:hidden h-full flex flex-col"
    >
      <div className="flex-1 overflow-y-auto overflow-x-hidden px-8 py-6">
        <div className="max-w-2xl space-y-8">
          {/* Header */}
          <div>
            <h2 className="text-xl font-semibold">{t('ai.title')}</h2>
            <p className="text-sm text-muted-foreground mt-1">
              {t('ai.description')}
            </p>
          </div>

          {/* ── Providers Section ── */}
          <div className="space-y-4">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Globe size={18} className="text-muted-foreground" />
                <h3 className="text-base font-medium">{t('ai.providers')}</h3>
              </div>
              <AddProviderDropdown onAdd={handleAddProvider} />
            </div>

            {providers.length === 0 ? (
              <div className="rounded-lg border border-dashed border-border/60 p-6 text-center">
                <Bot size={24} className="mx-auto text-muted-foreground mb-2" />
                <p className="text-sm text-muted-foreground">
                  {t('ai.providers.empty')}
                </p>
              </div>
            ) : (
              <div className="space-y-2">
                {providers.map((provider) => (
                  <ProviderCard
                    key={provider.id}
                    provider={provider}
                    isActive={provider.id === activeProviderId}
                    onToggleEnabled={(enabled) => {
                      if (enabled) {
                        // Activate this provider, deactivate all others
                        setActiveProviderId(provider.id);
                        if (provider.defaultModel) {
                          setActiveModelId(provider.defaultModel);
                        }
                        for (const p of providers) {
                          if (p.id === provider.id) {
                            if (!p.enabled) updateProvider(p.id, { enabled: true });
                          } else {
                            if (p.enabled) updateProvider(p.id, { enabled: false });
                          }
                        }
                      } else {
                        // Deactivate this provider
                        if (activeProviderId === provider.id) {
                          setActiveProviderId("");
                          setActiveModelId("");
                        }
                        updateProvider(provider.id, { enabled: false });
                      }
                    }}
                    onEdit={() =>
                      setEditingProviderId(
                        editingProviderId === provider.id ? null : provider.id,
                      )
                    }
                    onRemove={() => handleRemoveProvider(provider.id)}
                    onUpdate={(updates) => {
                      updateProvider(provider.id, updates);
                      // If this is the active provider and model changed, update activeModelId
                      if (provider.id === activeProviderId && updates.defaultModel !== undefined) {
                        setActiveModelId(updates.defaultModel || "");
                      }
                    }}
                    isEditing={editingProviderId === provider.id}
                    onCancelEdit={() => setEditingProviderId(null)}
                  />
                ))}
              </div>
            )}
          </div>

          {/* ── Codex Section ── */}
          <div className="space-y-4">
            <div className="flex items-center gap-2">
              <ProviderIconBadge providerId="openai" size="sm" />
              <h3 className="text-base font-medium">{t('ai.codex')}</h3>
            </div>

            <CodexConnectionCard
              pathInfo={codexPathInfo}
              isResolvingPath={isDiscovering || isResolvingCodex}
              customPath={codexCustomPath}
              onCustomPathChange={setCodexCustomPath}
              onRecheckPath={() => void handleCheckCustomPath("codex")}
              integration={codexIntegration}
              loginSession={codexLoginSession}
              isLoading={isCodexLoading}
              hasOpenAiProviderKey={hasOpenAiProviderKey}
              error={codexError}
              onRefresh={() => void refreshCodexIntegration()}
              onConnect={() => void handleStartCodexLogin()}
              onCancel={() => void handleCancelCodexLogin()}
              onOpenUrl={handleOpenCodexLoginUrl}
              onLogout={() => void handleCodexLogout()}
            />
          </div>

          {/* ── Claude Code Section ── */}
          <div className="space-y-4">
            <div className="flex items-center gap-2">
              <ProviderIconBadge providerId="claude" size="sm" />
              <h3 className="text-base font-medium">{t('ai.claude.title')}</h3>
            </div>

            <ClaudeCodeCard
              pathInfo={claudePathInfo}
              isResolvingPath={isDiscovering || isResolvingClaude}
              customPath={claudeCustomPath}
              onCustomPathChange={setClaudeCustomPath}
              onRecheckPath={() => void handleCheckCustomPath("claude")}
            />
          </div>

          {/* ── Default Agent Section ── */}
          {agentOptions.length > 1 && (
            <div className="space-y-4">
              <div className="flex items-center gap-2">
                <Bot size={18} className="text-muted-foreground" />
                <h3 className="text-base font-medium">{t('ai.defaultAgent')}</h3>
              </div>

              <div className="bg-muted/30 rounded-lg p-4">
                <SettingRow
                  label={t('ai.defaultAgent')}
                  description={t('ai.defaultAgent.description')}
                >
                  <Select
                    value={defaultAgentId}
                    options={agentOptions}
                    onChange={setDefaultAgentId}
                    className="w-48"
                  />
                </SettingRow>
              </div>
            </div>
          )}

          {/* ── Safety Section ── */}
          <div className="space-y-4">
            <div className="flex items-center gap-2">
              <Shield size={18} className="text-muted-foreground" />
              <h3 className="text-base font-medium">{t('ai.safety.title')}</h3>
            </div>

            <div className="bg-muted/30 rounded-lg p-4 space-y-1">
              <SettingRow
                label={t('ai.safety.permissionMode')}
                description={t('ai.safety.permissionMode.description')}
              >
                <Select
                  value={globalPermissionMode}
                  options={permissionModeOptions}
                  onChange={(val) => setGlobalPermissionMode(val as AIPermissionMode)}
                  className="w-64"
                />
              </SettingRow>

              <SettingRow
                label={t('ai.safety.commandTimeout')}
                description={t('ai.safety.commandTimeout.description')}
              >
                <div className="flex items-center gap-2">
                  <input
                    type="number"
                    value={commandTimeout}
                    onChange={(e) => {
                      const val = parseInt(e.target.value, 10);
                      if (!isNaN(val) && val > 0) setCommandTimeout(val);
                    }}
                    min={1}
                    max={3600}
                    className="w-20 h-9 rounded-md border border-input bg-background px-3 text-sm text-right focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  />
                  <span className="text-xs text-muted-foreground">{t('ai.safety.commandTimeout.unit')}</span>
                </div>
              </SettingRow>

              <SettingRow
                label={t('ai.safety.maxIterations')}
                description={t('ai.safety.maxIterations.description')}
              >
                <input
                  type="number"
                  value={maxIterations}
                  onChange={(e) => {
                    const val = parseInt(e.target.value, 10);
                    if (!isNaN(val) && val > 0) setMaxIterations(val);
                  }}
                  min={1}
                  max={100}
                  className="w-20 h-9 rounded-md border border-input bg-background px-3 text-sm text-right focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                />
              </SettingRow>
            </div>

            {/* ── Command Blocklist ── */}
            <div className="bg-muted/30 rounded-lg p-4 space-y-3">
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm font-medium">{t('ai.safety.blocklist')}</p>
                  <p className="text-xs text-muted-foreground">
                    {t('ai.safety.blocklist.description')}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-xs"
                  onClick={() => setCommandBlocklist([...DEFAULT_COMMAND_BLOCKLIST])}
                >
                  {t('ai.safety.blocklist.reset')}
                </Button>
              </div>

              <div className="space-y-1.5">
                {commandBlocklist.map((pattern, idx) => (
                  <div key={idx} className="flex items-center gap-2">
                    <input
                      type="text"
                      value={pattern}
                      onChange={(e) => {
                        const next = [...commandBlocklist];
                        next[idx] = e.target.value;
                        setCommandBlocklist(next);
                      }}
                      className="flex-1 h-8 rounded-md border border-input bg-background px-3 text-xs font-mono focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                      placeholder={t('ai.safety.blocklist.placeholder')}
                    />
                    <button
                      onClick={() => {
                        const next = commandBlocklist.filter((_, i) => i !== idx);
                        setCommandBlocklist(next);
                      }}
                      className="p-1 rounded hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors"
                    >
                      <X size={14} />
                    </button>
                  </div>
                ))}
              </div>

              <Button
                variant="outline"
                size="sm"
                className="text-xs"
                onClick={() => setCommandBlocklist([...commandBlocklist, ''])}
              >
                <Plus size={14} className="mr-1" />
                {t('ai.safety.blocklist.add')}
              </Button>
            </div>

            <p className="text-xs text-muted-foreground">
              {t('ai.safety.note')}
            </p>
          </div>
        </div>
      </div>
    </TabsContent>
  );
};

export default SettingsAITab;
