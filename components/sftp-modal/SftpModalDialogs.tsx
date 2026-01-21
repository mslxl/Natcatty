import React from "react";
import { Loader2 } from "lucide-react";
import { Button } from "../ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog";
import { Input } from "../ui/input";
import type { RemoteFile } from "../types";

interface PermissionsState {
  owner: { read: boolean; write: boolean; execute: boolean };
  group: { read: boolean; write: boolean; execute: boolean };
  others: { read: boolean; write: boolean; execute: boolean };
}

interface SftpModalDialogsProps {
  t: (key: string, params?: Record<string, unknown>) => string;
  showRenameDialog: boolean;
  setShowRenameDialog: (open: boolean) => void;
  renameTarget: RemoteFile | null;
  renameName: string;
  setRenameName: (value: string) => void;
  handleRename: () => void;
  isRenaming: boolean;
  showPermissionsDialog: boolean;
  setShowPermissionsDialog: (open: boolean) => void;
  permissionsTarget: RemoteFile | null;
  permissions: PermissionsState;
  togglePermission: (role: "owner" | "group" | "others", perm: "read" | "write" | "execute") => void;
  getOctalPermissions: () => string;
  getSymbolicPermissions: () => string;
  handleSavePermissions: () => void;
  isChangingPermissions: boolean;
}

export const SftpModalDialogs: React.FC<SftpModalDialogsProps> = ({
  t,
  showRenameDialog,
  setShowRenameDialog,
  renameTarget,
  renameName,
  setRenameName,
  handleRename,
  isRenaming,
  showPermissionsDialog,
  setShowPermissionsDialog,
  permissionsTarget,
  permissions,
  togglePermission,
  getOctalPermissions,
  getSymbolicPermissions,
  handleSavePermissions,
  isChangingPermissions,
}) => (
  <>
    <Dialog open={showRenameDialog} onOpenChange={setShowRenameDialog}>
      <DialogContent className="sm:max-w-[400px]">
        <DialogHeader>
          <DialogTitle>{t("sftp.rename.title")}</DialogTitle>
          <DialogDescription className="truncate">
            {renameTarget?.name}
          </DialogDescription>
        </DialogHeader>
        <div className="py-4">
          <Input
            value={renameName}
            onChange={(e) => setRenameName(e.target.value)}
            placeholder={t("sftp.rename.placeholder")}
            onKeyDown={(e) => {
              if (e.key === "Enter") handleRename();
            }}
            autoFocus
          />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => setShowRenameDialog(false)}>
            {t("common.cancel")}
          </Button>
          <Button onClick={handleRename} disabled={isRenaming || !renameName.trim()}>
            {isRenaming ? <Loader2 size={14} className="mr-2 animate-spin" /> : null}
            {t("common.apply")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog open={showPermissionsDialog} onOpenChange={setShowPermissionsDialog}>
      <DialogContent className="sm:max-w-[400px]">
        <DialogHeader>
          <DialogTitle>{t("sftp.permissions.title")}</DialogTitle>
          <DialogDescription className="truncate">
            {permissionsTarget?.name}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-4">
          <div className="space-y-3">
            {(["owner", "group", "others"] as const).map((role) => (
              <div key={role} className="flex items-center gap-4">
                <div className="w-16 text-sm font-medium">
                  {t(`sftp.permissions.${role}`)}
                </div>
                <div className="flex gap-3">
                  {(["read", "write", "execute"] as const).map((perm) => (
                    <label key={perm} className="flex items-center gap-1.5 cursor-pointer">
                      <input
                        type="checkbox"
                        checked={permissions[role][perm]}
                        onChange={() => togglePermission(role, perm)}
                        className="rounded border-border"
                      />
                      <span className="text-xs">
                        {perm === "read" ? "R" : perm === "write" ? "W" : "X"}
                      </span>
                    </label>
                  ))}
                </div>
              </div>
            ))}
          </div>
          <div className="flex items-center justify-between pt-2 border-t border-border/60">
            <div className="text-xs text-muted-foreground">
              {t("sftp.permissions.octal")}: <span className="font-mono text-foreground">{getOctalPermissions()}</span>
            </div>
            <div className="text-xs text-muted-foreground">
              {t("sftp.permissions.symbolic")}: <span className="font-mono text-foreground">{getSymbolicPermissions()}</span>
            </div>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => setShowPermissionsDialog(false)}>
            {t("common.cancel")}
          </Button>
          <Button onClick={handleSavePermissions} disabled={isChangingPermissions}>
            {isChangingPermissions ? <Loader2 size={14} className="mr-2 animate-spin" /> : null}
            {t("common.apply")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </>
);
