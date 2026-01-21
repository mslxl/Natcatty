import { useEffect } from "react";
import type { MutableRefObject } from "react";
import { netcattyBridge } from "../../infrastructure/services/netcattyBridge";

export const useSftpSessionCleanup = (sftpSessionsRef: MutableRefObject<Map<string, string>>) => {
  useEffect(() => {
    const sessionsRef = sftpSessionsRef.current;

    return () => {
      sessionsRef.forEach(async (sftpId) => {
        try {
          await netcattyBridge.get()?.closeSftp(sftpId);
        } catch {
          // Ignore errors when closing SFTP sessions during cleanup
        }
      });
    };
  }, [sftpSessionsRef]);
};
