import { useEffect, useState } from "react";
import { localStorageAdapter } from "../../infrastructure/persistence/localStorageAdapter";

export const useTreeExpandedState = (storageKey: string) => {
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(() => {
    const stored = localStorageAdapter.readString(storageKey);
    if (stored) {
      try {
        const paths = JSON.parse(stored) as string[];
        return new Set(paths);
      } catch {
        return new Set();
      }
    }
    return new Set();
  });

  useEffect(() => {
    const pathsArray = Array.from(expandedPaths);
    localStorageAdapter.writeString(storageKey, JSON.stringify(pathsArray));
  }, [storageKey, expandedPaths]);

  const togglePath = (path: string) => {
    const newExpanded = new Set(expandedPaths);
    if (newExpanded.has(path)) {
      newExpanded.delete(path);
    } else {
      newExpanded.add(path);
    }
    setExpandedPaths(newExpanded);
  };

  const expandAll = (allPaths: string[]) => {
    setExpandedPaths(new Set(allPaths));
  };

  const collapseAll = () => {
    setExpandedPaths(new Set());
  };

  return {
    expandedPaths,
    togglePath,
    expandAll,
    collapseAll,
  };
};