import { useCallback, useState } from "react";
import type { RemoteFile } from "../../types";
import { toast } from "../../ui/toast";

interface UseSftpModalPermissionsParams {
  currentPath: string;
  isLocalSession: boolean;
  joinPath: (base: string, name: string) => string;
  ensureSftp: () => Promise<string>;
  loadFiles: (path: string, options?: { force?: boolean }) => Promise<void>;
  chmodSftp: (sftpId: string, path: string, permissions: string) => Promise<void>;
  statSftp: (sftpId: string, path: string) => Promise<{ permissions?: string }>;
  t: (key: string, params?: Record<string, unknown>) => string;
}

interface PermissionsState {
  owner: { read: boolean; write: boolean; execute: boolean };
  group: { read: boolean; write: boolean; execute: boolean };
  others: { read: boolean; write: boolean; execute: boolean };
}

interface UseSftpModalPermissionsResult {
  showPermissionsDialog: boolean;
  setShowPermissionsDialog: (open: boolean) => void;
  permissionsTarget: RemoteFile | null;
  permissions: PermissionsState;
  isChangingPermissions: boolean;
  openPermissionsDialog: (file: RemoteFile) => Promise<void>;
  togglePermission: (role: "owner" | "group" | "others", perm: "read" | "write" | "execute") => void;
  getOctalPermissions: () => string;
  getSymbolicPermissions: () => string;
  handleSavePermissions: () => Promise<void>;
}

export const useSftpModalPermissions = ({
  currentPath,
  isLocalSession,
  joinPath,
  ensureSftp,
  loadFiles,
  chmodSftp,
  statSftp,
  t,
}: UseSftpModalPermissionsParams): UseSftpModalPermissionsResult => {
  const [showPermissionsDialog, setShowPermissionsDialog] = useState(false);
  const [permissionsTarget, setPermissionsTarget] = useState<RemoteFile | null>(null);
  const [permissions, setPermissions] = useState<PermissionsState>({
    owner: { read: false, write: false, execute: false },
    group: { read: false, write: false, execute: false },
    others: { read: false, write: false, execute: false },
  });
  const [isChangingPermissions, setIsChangingPermissions] = useState(false);

  const parsePermissions = useCallback((perms: string | undefined) => {
    const defaultPerms = {
      owner: { read: false, write: false, execute: false },
      group: { read: false, write: false, execute: false },
      others: { read: false, write: false, execute: false },
    };
    if (!perms) return defaultPerms;

    if (/^[0-7]{3,4}$/.test(perms)) {
      const octal = perms.length === 4 ? perms.slice(1) : perms;
      const ownerBits = parseInt(octal[0], 10);
      const groupBits = parseInt(octal[1], 10);
      const othersBits = parseInt(octal[2], 10);
      return {
        owner: {
          read: (ownerBits & 4) !== 0,
          write: (ownerBits & 2) !== 0,
          execute: (ownerBits & 1) !== 0,
        },
        group: {
          read: (groupBits & 4) !== 0,
          write: (groupBits & 2) !== 0,
          execute: (groupBits & 1) !== 0,
        },
        others: {
          read: (othersBits & 4) !== 0,
          write: (othersBits & 2) !== 0,
          execute: (othersBits & 1) !== 0,
        },
      };
    }

    const pStr = perms.length === 10 ? perms.slice(1) : perms;
    if (pStr.length >= 9) {
      return {
        owner: {
          read: pStr[0] === "r",
          write: pStr[1] === "w",
          execute: pStr[2] === "x" || pStr[2] === "s",
        },
        group: {
          read: pStr[3] === "r",
          write: pStr[4] === "w",
          execute: pStr[5] === "x" || pStr[5] === "s",
        },
        others: {
          read: pStr[6] === "r",
          write: pStr[7] === "w",
          execute: pStr[8] === "x" || pStr[8] === "t",
        },
      };
    }
    return defaultPerms;
  }, []);

  const openPermissionsDialog = useCallback(async (file: RemoteFile) => {
    if (isLocalSession) {
      toast.error("Permissions not available for local files", "SFTP");
      return;
    }
    setPermissionsTarget(file);

    let permsStr = file.permissions;
    try {
      const fullPath = joinPath(currentPath, file.name);
      const stat = await statSftp(await ensureSftp(), fullPath);
      if (stat.permissions) {
        permsStr = stat.permissions;
      }
    } catch (e) {
      console.warn("Failed to fetch file permissions:", e);
    }

    setPermissions(parsePermissions(permsStr));
    setShowPermissionsDialog(true);
  }, [currentPath, ensureSftp, isLocalSession, joinPath, parsePermissions, statSftp]);

  const togglePermission = useCallback(
    (role: "owner" | "group" | "others", perm: "read" | "write" | "execute") => {
      setPermissions((prev) => ({
        ...prev,
        [role]: { ...prev[role], [perm]: !prev[role][perm] },
      }));
    },
    [],
  );

  const getOctalPermissions = useCallback(() => {
    const getNum = (p: { read: boolean; write: boolean; execute: boolean }) =>
      (p.read ? 4 : 0) + (p.write ? 2 : 0) + (p.execute ? 1 : 0);
    return `${getNum(permissions.owner)}${getNum(permissions.group)}${getNum(permissions.others)}`;
  }, [permissions]);

  const getSymbolicPermissions = useCallback(() => {
    const getSym = (p: { read: boolean; write: boolean; execute: boolean }) =>
      `${p.read ? "r" : "-"}${p.write ? "w" : "-"}${p.execute ? "x" : "-"}`;
    return (
      getSym(permissions.owner) +
      getSym(permissions.group) +
      getSym(permissions.others)
    );
  }, [permissions]);

  const handleSavePermissions = useCallback(async () => {
    if (!permissionsTarget || isChangingPermissions) return;
    setIsChangingPermissions(true);
    try {
      const fullPath = joinPath(currentPath, permissionsTarget.name);
      await chmodSftp(await ensureSftp(), fullPath, getOctalPermissions());
      setShowPermissionsDialog(false);
      setPermissionsTarget(null);
      await loadFiles(currentPath, { force: true });
      toast.success(t("sftp.permissions.success"), "SFTP");
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t("sftp.permissions.failed"),
        "SFTP",
      );
    } finally {
      setIsChangingPermissions(false);
    }
  }, [chmodSftp, currentPath, ensureSftp, getOctalPermissions, isChangingPermissions, joinPath, loadFiles, permissionsTarget, t]);

  return {
    showPermissionsDialog,
    setShowPermissionsDialog,
    permissionsTarget,
    permissions,
    isChangingPermissions,
    openPermissionsDialog,
    togglePermission,
    getOctalPermissions,
    getSymbolicPermissions,
    handleSavePermissions,
  };
};
