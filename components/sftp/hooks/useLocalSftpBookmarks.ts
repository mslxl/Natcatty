import { useCallback, useEffect, useMemo, useState } from "react";
import type { SftpBookmark } from "../../../domain/models";
import { localStorageAdapter } from "../../../infrastructure/persistence/localStorageAdapter";
import { STORAGE_KEY_SFTP_LOCAL_BOOKMARKS } from "../../../infrastructure/config/storageKeys";

interface UseLocalSftpBookmarksParams {
    currentPath: string | undefined;
}

export const useLocalSftpBookmarks = ({
    currentPath,
}: UseLocalSftpBookmarksParams) => {
    const [bookmarks, setBookmarks] = useState<SftpBookmark[]>(() =>
        localStorageAdapter.read<SftpBookmark[]>(STORAGE_KEY_SFTP_LOCAL_BOOKMARKS) ?? [],
    );

    useEffect(() => {
        localStorageAdapter.write(STORAGE_KEY_SFTP_LOCAL_BOOKMARKS, bookmarks);
    }, [bookmarks]);

    const isCurrentPathBookmarked = useMemo(
        () => !!currentPath && bookmarks.some((b) => b.path === currentPath),
        [currentPath, bookmarks],
    );

    const toggleBookmark = useCallback(() => {
        if (!currentPath) return;
        if (isCurrentPathBookmarked) {
            setBookmarks((prev) => prev.filter((b) => b.path !== currentPath));
        } else {
            const label =
                currentPath === "/"
                    ? "/"
                    : currentPath.split("/").filter(Boolean).pop() || currentPath;
            const newBookmark: SftpBookmark = {
                id: `bm-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`,
                path: currentPath,
                label,
            };
            setBookmarks((prev) => [...prev, newBookmark]);
        }
    }, [currentPath, isCurrentPathBookmarked]);

    const deleteBookmark = useCallback((id: string) => {
        setBookmarks((prev) => prev.filter((b) => b.id !== id));
    }, []);

    return {
        bookmarks,
        isCurrentPathBookmarked,
        toggleBookmark,
        deleteBookmark,
    };
};
