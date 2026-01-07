/**
 * useSftpFileAssociations - Hook for managing SFTP file opener associations
 */
import { useCallback, useEffect, useState } from 'react';
import { STORAGE_KEY_SFTP_FILE_ASSOCIATIONS } from '../../infrastructure/config/storageKeys';
import { localStorageAdapter } from '../../infrastructure/persistence/localStorageAdapter';
import type { FileAssociation, FileOpenerType } from '../../lib/sftpFileUtils';
import { getFileExtension } from '../../lib/sftpFileUtils';

export interface FileAssociationsMap {
  [extension: string]: FileOpenerType;
}

export function useSftpFileAssociations() {
  const [associations, setAssociations] = useState<FileAssociationsMap>(() => {
    const stored = localStorageAdapter.read<FileAssociationsMap>(STORAGE_KEY_SFTP_FILE_ASSOCIATIONS);
    return stored || {};
  });

  // Persist associations to localStorage
  useEffect(() => {
    localStorageAdapter.write(STORAGE_KEY_SFTP_FILE_ASSOCIATIONS, associations);
  }, [associations]);

  /**
   * Get the opener type for a file based on its extension
   */
  const getOpenerForFile = useCallback((fileName: string): FileOpenerType | null => {
    const ext = getFileExtension(fileName);
    return associations[ext] || null;
  }, [associations]);

  /**
   * Set the opener type for a specific extension
   */
  const setOpenerForExtension = useCallback((extension: string, openerType: FileOpenerType) => {
    setAssociations(prev => ({
      ...prev,
      [extension.toLowerCase()]: openerType,
    }));
  }, []);

  /**
   * Remove the association for a specific extension
   */
  const removeAssociation = useCallback((extension: string) => {
    setAssociations(prev => {
      const next = { ...prev };
      delete next[extension.toLowerCase()];
      return next;
    });
  }, []);

  /**
   * Get all associations as an array
   */
  const getAllAssociations = useCallback((): FileAssociation[] => {
    return Object.entries(associations).map(([extension, openerType]) => ({
      extension,
      openerType: openerType as FileOpenerType,
    }));
  }, [associations]);

  /**
   * Clear all associations
   */
  const clearAllAssociations = useCallback(() => {
    setAssociations({});
  }, []);

  return {
    associations,
    getOpenerForFile,
    setOpenerForExtension,
    removeAssociation,
    getAllAssociations,
    clearAllAssociations,
  };
}
