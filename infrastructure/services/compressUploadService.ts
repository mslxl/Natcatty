/**
 * Compressed Upload Service
 * 
 * Provides compressed folder upload functionality using tar compression
 */

import { netcattyBridge } from "./netcattyBridge";

export interface CompressUploadOptions {
  compressionId: string;
  folderPath: string;
  targetPath: string;
  sftpId: string;
  folderName: string;
}

export interface CompressUploadProgress {
  phase: 'compressing' | 'uploading' | 'extracting';
  transferred: number;
  total: number;
}

export interface CompressUploadSupport {
  supported: boolean;
  localTar: boolean;
  remoteTar: boolean;
  error?: string;
}

export type CompressUploadProgressCallback = (phase: string, transferred: number, total: number) => void;
export type CompressUploadCompleteCallback = () => void;
export type CompressUploadErrorCallback = (error: string) => void;

/**
 * Start a compressed folder upload
 */
export async function startCompressedUpload(
  options: CompressUploadOptions,
  onProgress?: CompressUploadProgressCallback,
  onComplete?: CompressUploadCompleteCallback,
  onError?: CompressUploadErrorCallback
): Promise<{ compressionId: string; success?: boolean; error?: string }> {
  const bridge = netcattyBridge.get();
  if (!bridge?.startCompressedUpload) {
    throw new Error("Compressed upload not available");
  }

  try {
    return await bridge.startCompressedUpload(options, onProgress, onComplete, onError);
  } catch (error) {
    const errorMessage = error instanceof Error ? error.message : String(error);
    return {
      compressionId: options.compressionId,
      success: false,
      error: errorMessage
    };
  }
}

/**
 * Cancel a compressed upload
 */
export async function cancelCompressedUpload(compressionId: string): Promise<{ success: boolean }> {
  const bridge = netcattyBridge.get();
  if (!bridge?.cancelCompressedUpload) {
    throw new Error("Compressed upload not available");
  }

  return bridge.cancelCompressedUpload(compressionId);
}

/**
 * Check if compressed upload is supported for a given SFTP session
 */
export async function checkCompressedUploadSupport(sftpId: string): Promise<CompressUploadSupport> {
  const bridge = netcattyBridge.get();
  if (!bridge?.checkCompressedUploadSupport) {
    return {
      supported: false,
      localTar: false,
      remoteTar: false,
      error: "Compressed upload not available"
    };
  }

  return bridge.checkCompressedUploadSupport(sftpId);
}
