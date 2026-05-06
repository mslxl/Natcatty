import { AlertTriangle, Check, Copy, Edit2, Globe, Plus, Search, Trash2 } from "lucide-react";
import React, { useMemo, useState } from "react";
import { useI18n } from "../application/i18n/I18nProvider";
import { removeProxyProfileReferences } from "../domain/proxyProfiles";
import { cn } from "../lib/utils";
import type { GroupConfig, Host, ProxyConfig, ProxyProfile } from "../types";
import {
  AsidePanel,
  AsidePanelContent,
  AsidePanelFooter,
} from "./ui/aside-panel";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Card } from "./ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import { Input } from "./ui/input";
import { toast } from "./ui/toast";

interface ProxyProfilesManagerProps {
  proxyProfiles: ProxyProfile[];
  hosts: Host[];
  groupConfigs: GroupConfig[];
  onUpdateProxyProfiles: (profiles: ProxyProfile[]) => void;
  onUpdateHosts: (hosts: Host[]) => void;
  onUpdateGroupConfigs: (configs: GroupConfig[]) => void;
}

const createDraftProfile = (): ProxyProfile => {
  const now = Date.now();
  return {
    id: crypto.randomUUID(),
    label: "",
    config: {
      type: "http",
      host: "",
      port: 8080,
    },
    createdAt: now,
    updatedAt: now,
  };
};

const getProfileUsageCount = (
  profileId: string,
  hosts: Host[],
  groupConfigs: GroupConfig[],
): number =>
  hosts.filter((host) => host.proxyProfileId === profileId).length +
  groupConfigs.filter((config) => config.proxyProfileId === profileId).length;

export const ProxyProfilesManager: React.FC<ProxyProfilesManagerProps> = ({
  proxyProfiles,
  hosts,
  groupConfigs,
  onUpdateProxyProfiles,
  onUpdateHosts,
  onUpdateGroupConfigs,
}) => {
  const { t } = useI18n();
  const [search, setSearch] = useState("");
  const [draft, setDraft] = useState<ProxyProfile | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ProxyProfile | null>(null);

  const usageByProfileId = useMemo(() => {
    const map = new Map<string, number>();
    for (const profile of proxyProfiles) {
      map.set(profile.id, getProfileUsageCount(profile.id, hosts, groupConfigs));
    }
    return map;
  }, [groupConfigs, hosts, proxyProfiles]);

  const filteredProfiles = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return proxyProfiles;
    return proxyProfiles.filter((profile) =>
      profile.label.toLowerCase().includes(q) ||
      profile.config.host.toLowerCase().includes(q) ||
      profile.config.type.toLowerCase().includes(q),
    );
  }, [proxyProfiles, search]);

  const updateDraftConfig = (field: keyof ProxyConfig, value: string | number) => {
    setDraft((prev) => {
      if (!prev) return prev;
      return {
        ...prev,
        config: {
          ...prev.config,
          [field]: value,
        },
      };
    });
  };

  const openCreate = () => {
    setDraft(createDraftProfile());
  };

  const openEdit = (profile: ProxyProfile) => {
    setDraft({
      ...profile,
      config: { ...profile.config },
    });
  };

  const duplicateProfile = (profile: ProxyProfile) => {
    const now = Date.now();
    onUpdateProxyProfiles([
      ...proxyProfiles,
      {
        ...profile,
        id: crypto.randomUUID(),
        label: t("proxyProfiles.copyName", { name: profile.label }),
        config: { ...profile.config },
        createdAt: now,
        updatedAt: now,
      },
    ]);
  };

  const saveDraft = () => {
    if (!draft) return;
    const label = draft.label.trim();
    const host = draft.config.host.trim();
    if (!label || !host || !draft.config.port) {
      toast.error(t("proxyProfiles.error.required"));
      return;
    }

    const saved: ProxyProfile = {
      ...draft,
      label,
      config: {
        ...draft.config,
        host,
        port: Number(draft.config.port),
        username: draft.config.username?.trim() || undefined,
        password: draft.config.password || undefined,
      },
      updatedAt: Date.now(),
    };

    onUpdateProxyProfiles(
      proxyProfiles.some((profile) => profile.id === saved.id)
        ? proxyProfiles.map((profile) => profile.id === saved.id ? saved : profile)
        : [...proxyProfiles, saved],
    );
    setDraft(null);
  };

  const confirmDelete = () => {
    if (!deleteTarget) return;
    const cleaned = removeProxyProfileReferences(deleteTarget.id, {
      hosts,
      groupConfigs,
    });
    onUpdateProxyProfiles(proxyProfiles.filter((profile) => profile.id !== deleteTarget.id));
    onUpdateHosts(cleaned.hosts);
    onUpdateGroupConfigs(cleaned.groupConfigs);
    setDeleteTarget(null);
  };

  return (
    <div className="h-full flex relative">
      <div className={cn("flex-1 flex flex-col min-h-0 transition-all duration-200", draft && "mr-[380px]")}>
        <div className="h-14 px-4 py-2 flex items-center gap-3 bg-secondary/80 supports-[backdrop-filter]:backdrop-blur-sm border-b border-border/50 shrink-0">
          <div className="relative flex-1 max-w-md">
            <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t("proxyProfiles.search.placeholder")}
              className="h-10 pl-9"
            />
          </div>
          <div className="ml-auto">
            <Button onClick={openCreate}>
              <Plus size={14} className="mr-2" />
              {t("proxyProfiles.action.add")}
            </Button>
          </div>
        </div>

        <div className="flex-1 min-h-0 overflow-auto p-4">
          {filteredProfiles.length === 0 ? (
            <div className="h-full flex flex-col items-center justify-center text-muted-foreground">
              <div className="h-16 w-16 rounded-2xl bg-secondary/80 flex items-center justify-center mb-4">
                <Globe size={32} className="opacity-60" />
              </div>
              <h3 className="text-lg font-semibold text-foreground mb-2">
                {t("proxyProfiles.empty.title")}
              </h3>
              <p className="text-sm text-center max-w-sm">
                {t("proxyProfiles.empty.desc")}
              </p>
              <Button className="mt-4" onClick={openCreate}>
                <Plus size={14} className="mr-2" />
                {t("proxyProfiles.action.add")}
              </Button>
            </div>
          ) : (
            <div className="grid gap-3 grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
              {filteredProfiles.map((profile) => {
                const usageCount = usageByProfileId.get(profile.id) ?? 0;
                return (
                  <Card
                    key={profile.id}
                    className="p-3 bg-card border-border/80 hover:border-primary/40 transition-colors"
                  >
                    <button
                      type="button"
                      className="w-full min-w-0 text-left"
                      onClick={() => openEdit(profile)}
                    >
                      <div className="flex items-start gap-3">
                        <div className="h-10 w-10 rounded-md bg-secondary/70 flex items-center justify-center shrink-0">
                          <Globe size={18} className="text-muted-foreground" />
                        </div>
                        <div className="min-w-0 flex-1">
                          <div className="flex items-center gap-2 min-w-0">
                            <p className="font-medium truncate">{profile.label}</p>
                            <Badge variant="secondary" className="text-[10px] shrink-0">
                              {profile.config.type.toUpperCase()}
                            </Badge>
                          </div>
                          <p className="text-sm text-muted-foreground truncate">
                            {profile.config.host}:{profile.config.port}
                          </p>
                          <p className="text-xs text-muted-foreground mt-1">
                            {t("proxyProfiles.usage", { count: usageCount })}
                          </p>
                        </div>
                      </div>
                    </button>
                    <div className="flex items-center gap-1 mt-3">
                      <Button variant="ghost" size="sm" className="h-8 px-2" onClick={() => openEdit(profile)}>
                        <Edit2 size={14} />
                      </Button>
                      <Button variant="ghost" size="sm" className="h-8 px-2" onClick={() => duplicateProfile(profile)}>
                        <Copy size={14} />
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-8 px-2 text-destructive ml-auto"
                        onClick={() => setDeleteTarget(profile)}
                      >
                        <Trash2 size={14} />
                      </Button>
                    </div>
                  </Card>
                );
              })}
            </div>
          )}
        </div>
      </div>

      {draft && (
        <AsidePanel
          open={true}
          onClose={() => setDraft(null)}
          title={draft.label || t("proxyProfiles.panel.newTitle")}
        >
          <AsidePanelContent>
            <Card className="p-3 space-y-3 bg-card border-border/80">
              <Input
                value={draft.label}
                onChange={(event) => setDraft({ ...draft, label: event.target.value })}
                placeholder={t("proxyProfiles.field.name")}
                className="h-10"
              />
            </Card>

            <Card className="p-3 space-y-3 bg-card border-border/80">
              <div className="flex items-center justify-between">
                <p className="text-xs font-semibold">{t("field.type")}</p>
                <div className="flex gap-2">
                  <Button
                    variant={draft.config.type === "http" ? "secondary" : "ghost"}
                    size="sm"
                    className={cn("h-8", draft.config.type === "http" && "bg-primary/15")}
                    onClick={() => updateDraftConfig("type", "http")}
                  >
                    <Check size={14} className={cn("mr-1", draft.config.type !== "http" && "opacity-0")} />
                    HTTP
                  </Button>
                  <Button
                    variant={draft.config.type === "socks5" ? "secondary" : "ghost"}
                    size="sm"
                    className={cn("h-8", draft.config.type === "socks5" && "bg-primary/15")}
                    onClick={() => updateDraftConfig("type", "socks5")}
                  >
                    <Check size={14} className={cn("mr-1", draft.config.type !== "socks5" && "opacity-0")} />
                    SOCKS5
                  </Button>
                </div>
              </div>

              <div className="flex gap-2">
                <Input
                  value={draft.config.host}
                  onChange={(event) => updateDraftConfig("host", event.target.value)}
                  placeholder={t("hostDetails.proxyPanel.hostPlaceholder")}
                  className="h-10 flex-1"
                />
                <Input
                  type="number"
                  value={draft.config.port || ""}
                  onChange={(event) => updateDraftConfig("port", parseInt(event.target.value) || 0)}
                  placeholder="3128"
                  className="h-10 w-24 text-center"
                />
              </div>
            </Card>

            <Card className="p-3 space-y-3 bg-card border-border/80">
              <div className="flex items-center justify-between">
                <p className="text-xs font-semibold">{t("hostDetails.proxyPanel.credentials")}</p>
                <Badge variant="secondary" className="text-xs">{t("common.optional")}</Badge>
              </div>
              <Input
                value={draft.config.username || ""}
                onChange={(event) => updateDraftConfig("username", event.target.value)}
                placeholder={t("hostDetails.proxyPanel.usernamePlaceholder")}
                className="h-10"
              />
              <Input
                type="password"
                value={draft.config.password || ""}
                onChange={(event) => updateDraftConfig("password", event.target.value)}
                placeholder={t("hostDetails.proxyPanel.passwordPlaceholder")}
                className="h-10"
              />
            </Card>
          </AsidePanelContent>
          <AsidePanelFooter>
            <Button className="w-full" onClick={saveDraft}>
              {t("common.save")}
            </Button>
          </AsidePanelFooter>
        </AsidePanel>
      )}

      <Dialog open={Boolean(deleteTarget)} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <AlertTriangle size={18} className="text-destructive" />
              {t("proxyProfiles.delete.title")}
            </DialogTitle>
            <DialogDescription>
              {deleteTarget
                ? t("proxyProfiles.delete.desc", {
                  name: deleteTarget.label,
                  count: usageByProfileId.get(deleteTarget.id) ?? 0,
                })
                : ""}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" onClick={confirmDelete}>
              {t("action.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
};

export default ProxyProfilesManager;
