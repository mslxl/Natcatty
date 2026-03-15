/**
 * useConversationExport — Encapsulates conversation export logic for the AI chat panel.
 *
 * Handles:
 * - Export in markdown, JSON, and plain text formats
 * - Object URL lifecycle management (creation, revocation, cleanup on unmount)
 */

import React, { useCallback, useEffect, useRef } from 'react';
import type { AISession } from '../../../infrastructure/ai/types';
import { exportAsMarkdown, exportAsJSON, exportAsPlainText, getExportFilename } from '../../../infrastructure/ai/conversationExport';

// -------------------------------------------------------------------
// Hook return type
// -------------------------------------------------------------------

export interface UseConversationExportReturn {
  /** Trigger a download of the active session in the given format. */
  handleExport: (format: 'md' | 'json' | 'txt') => void;
  /** Ref to active object URLs for cleanup on unmount (exposed for the parent cleanup effect). */
  activeObjectUrlsRef: React.MutableRefObject<Set<string>>;
}

// -------------------------------------------------------------------
// Hook implementation
// -------------------------------------------------------------------

export function useConversationExport(
  activeSession: AISession | null,
): UseConversationExportReturn {
  // Ref to track active object URLs for cleanup on unmount (Issue #19)
  const activeObjectUrlsRef = useRef<Set<string>>(new Set());

  // Clean up object URLs on unmount
  useEffect(() => {
    const urls = activeObjectUrlsRef.current;
    return () => {
      urls.forEach(url => URL.revokeObjectURL(url));
      urls.clear();
    };
  }, []);

  const handleExport = useCallback((format: 'md' | 'json' | 'txt') => {
    if (!activeSession) return;
    let content: string;
    switch (format) {
      case 'md': content = exportAsMarkdown(activeSession); break;
      case 'json': content = exportAsJSON(activeSession); break;
      case 'txt': content = exportAsPlainText(activeSession); break;
    }
    const filename = getExportFilename(activeSession, format);
    // Create a download blob
    const blob = new Blob([content], { type: 'text/plain;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    // Track URL for cleanup on unmount (Issue #19)
    activeObjectUrlsRef.current.add(url);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    // Revoke after a generous delay to ensure download completes, then remove from tracking set
    const revokeTimeout = setTimeout(() => {
      URL.revokeObjectURL(url);
      activeObjectUrlsRef.current.delete(url);
    }, 60_000); // 60 seconds to be safe for large files
    // If component unmounts before timeout, cleanup effect will revoke it
    void revokeTimeout; // suppress unused warning
  }, [activeSession]);

  return {
    handleExport,
    activeObjectUrlsRef,
  };
}
