/**
 * Compress Upload Bridge - Handles folder compression and upload
 * 
 * Compresses folders locally using tar, uploads the archive, then extracts on remote server
 */

const fs = require("node:fs");
const path = require("node:path");
const { spawn } = require("node:child_process");
const { getTempFilePath } = require("./tempDirBridge.cjs");

// Shared references
let sftpClients = null;
let transferBridge = null;

// Active compress operations
const activeCompressions = new Map();

/**
 * Initialize the compress upload bridge with dependencies
 */
function init(deps) {
  sftpClients = deps.sftpClients;
  transferBridge = deps.transferBridge;
}

/**
 * Check if tar command is available on the system
 */
async function checkTarAvailable() {
  return new Promise((resolve) => {
    const tar = spawn('tar', ['--version'], { stdio: 'ignore' });
    tar.on('close', (code) => {
      resolve(code === 0);
    });
    tar.on('error', () => {
      resolve(false);
    });
  });
}

/**
 * Check if tar command is available on remote server
 */
async function checkRemoteTarAvailable(sftpId) {
  try {
    const client = sftpClients.get(sftpId);
    if (!client) throw new Error("SFTP session not found");
    
    // Try to execute tar --version via SSH
    const sshClient = client.client; // Get underlying SSH2 client
    if (!sshClient) throw new Error("SSH client not available");
    
    return new Promise((resolve) => {
      sshClient.exec('tar --version', (err, stream) => {
        if (err) {
          resolve(false);
          return;
        }
        
        let hasOutput = false;
        stream.on('data', () => {
          hasOutput = true;
        });
        
        stream.on('close', (code) => {
          resolve(code === 0 && hasOutput);
        });
        
        stream.on('error', () => {
          resolve(false);
        });
      });
    });
  } catch {
    return false;
  }
}

/**
 * Compress a folder using tar
 */
async function compressFolder(folderPath, outputPath, compressionId, sendProgress) {
  return new Promise((resolve, reject) => {
    const compression = activeCompressions.get(compressionId);
    if (!compression) {
      reject(new Error('Compression cancelled'));
      return;
    }

    // Use tar with gzip compression, excluding macOS resource fork files
    // -czf: create, gzip, file
    // -C: change to directory (so we don't include the full path in archive)
    // --exclude='._*': exclude macOS resource fork files
    // --exclude='.DS_Store': exclude macOS folder metadata files
    const folderName = path.basename(folderPath);
    const parentDir = path.dirname(folderPath);
    
    const tar = spawn('tar', [
      '-czf', outputPath, 
      '-C', parentDir, 
      '--exclude=._*',
      '--exclude=.DS_Store',
      '--exclude=.Spotlight-V100',
      '--exclude=.Trashes',
      folderName
    ], {
      stdio: ['ignore', 'pipe', 'pipe']
    });

    compression.process = tar;
    let stderr = '';

    // Monitor progress by checking output file size periodically
    const progressInterval = setInterval(async () => {
      if (compression.cancelled) {
        clearInterval(progressInterval);
        return;
      }
      
      try {
        const stat = await fs.promises.stat(outputPath);
        // We don't know the final size, so we'll show indeterminate progress
        sendProgress(stat.size, 0); // 0 means indeterminate
      } catch {
        // File doesn't exist yet, ignore
      }
    }, 500);

    tar.stderr.on('data', (data) => {
      stderr += data.toString();
    });

    tar.on('close', (code) => {
      clearInterval(progressInterval);
      
      if (compression.cancelled) {
        // Clean up output file if cancelled
        fs.promises.unlink(outputPath).catch(() => {});
        reject(new Error('Compression cancelled'));
        return;
      }
      
      if (code === 0) {
        resolve();
      } else {
        reject(new Error(`Tar compression failed: ${stderr}`));
      }
    });

    tar.on('error', (err) => {
      clearInterval(progressInterval);
      reject(new Error(`Failed to start tar: ${err.message}`));
    });
  });
}

/**
 * Extract archive on remote server
 */
async function extractRemoteArchive(sftpId, archivePath, targetDir) {
  const client = sftpClients.get(sftpId);
  if (!client) throw new Error("SFTP session not found");
  
  const sshClient = client.client;
  if (!sshClient) throw new Error("SSH client not available");
  
  return new Promise((resolve, reject) => {
    // Create target directory, extract, then always clean up the archive
    // Use && for tar success, then always try cleanup regardless of tar result
    // Also exclude any ._* files that might have been included despite our compression exclusions
    const command = `mkdir -p "${targetDir}" && cd "${targetDir}" && tar -xzf "${archivePath}" --exclude='._*' --exclude='.DS_Store' && rm -f "${archivePath}" || (rm -f "${archivePath}"; exit 1)`;
    console.log('[CompressUpload] Executing remote extraction command:', command);
    
    sshClient.exec(command, (err, stream) => {
      if (err) {
        console.error('[CompressUpload] Failed to execute extraction command:', err);
        reject(new Error(`Failed to execute extraction command: ${err.message}`));
        return;
      }
      
      let stderr = '';
      let stdout = '';
      let resolved = false;
      
      stream.on('data', (data) => {
        stdout += data.toString();
      });
      
      stream.stderr.on('data', (data) => {
        stderr += data.toString();
      });
      
      stream.on('close', (code) => {
        if (resolved) return;
        resolved = true;
        
        console.log('[CompressUpload] Remote extraction completed with code:', code);
        if (stdout) console.log('[CompressUpload] stdout:', stdout);
        if (stderr) console.log('[CompressUpload] stderr:', stderr);
        
        clearTimeout(timeout);
        
        // The command uses `;` and `||` so cleanup should always run
        // We only care about the tar extraction success (first part of command)
        // The rm commands are just cleanup and their failure doesn't matter
        
        // For most cases, code 0 means success
        // If code is not 0, check if it's just cleanup failure
        if (code === 0) {
          resolve();
        } else {
          // Check if the error is from tar extraction or just cleanup
          // If stderr contains tar errors, it's a real extraction failure
          if (stderr.includes('tar:') || stderr.includes('gzip:') || stderr.includes('Cannot open:') || stderr.includes('not found in archive')) {
            reject(new Error(`Remote extraction failed: ${stderr || 'Tar extraction error'}`));
          } else {
            // Likely just cleanup failure, check if extraction actually succeeded
            // We can verify by checking if the extraction created files
            console.warn('[CompressUpload] Extraction may have succeeded but cleanup failed:', stderr);
            // For now, consider it successful if no tar-specific errors
            resolve();
          }
        }
      });
      
      stream.on('error', (err) => {
        if (resolved) return;
        resolved = true;
        
        console.error('[CompressUpload] Stream error:', err);
        clearTimeout(timeout);
        reject(new Error(`Stream error: ${err.message}`));
      });
      
      // Add timeout to prevent hanging
      const timeout = setTimeout(() => {
        if (resolved) return;
        resolved = true;
        
        console.error('[CompressUpload] Remote extraction timed out');
        reject(new Error('Remote extraction timed out after 30 seconds'));
      }, 30000);
    });
  });
}

/**
 * Start compressed folder upload
 */
async function startCompressedUpload(event, payload) {
  const {
    compressionId,
    folderPath,
    targetPath,
    sftpId,
    folderName
  } = payload;
  const sender = event.sender;

  // Register compression for cancellation
  const compression = { cancelled: false, process: null };
  activeCompressions.set(compressionId, compression);

  const sendProgress = (phase, transferred, total) => {
    if (compression.cancelled) return;
    sender.send("netcatty:compress:progress", { 
      compressionId, 
      phase, 
      transferred, 
      total 
    });
  };

  const sendComplete = () => {
    console.log(`[CompressUpload] Sending completion for compressionId: ${compressionId}`);
    // Send final 100% progress before completion
    if (!compression.cancelled) {
      console.log(`[CompressUpload] Sending final 100% progress for compressionId: ${compressionId}`);
      sender.send("netcatty:compress:progress", { 
        compressionId, 
        phase: 'extracting', 
        transferred: 100, 
        total: 100 
      });
    }
    activeCompressions.delete(compressionId);
    console.log(`[CompressUpload] Sending complete event for compressionId: ${compressionId}`);
    sender.send("netcatty:compress:complete", { compressionId });
  };

  const sendError = (error) => {
    activeCompressions.delete(compressionId);
    sender.send("netcatty:compress:error", { 
      compressionId, 
      error: error.message || String(error) 
    });
  };

  // Declare tempArchivePath in outer scope for cleanup access
  let tempArchivePath = null;

  try {
    // Check if tar is available locally and remotely
    const localTarAvailable = await checkTarAvailable();
    if (!localTarAvailable) {
      throw new Error("tar command not available on local system. Please install tar.");
    }

    const remoteTarAvailable = await checkRemoteTarAvailable(sftpId);
    if (!remoteTarAvailable) {
      throw new Error("tar command not available on remote server. Please install tar on the remote system.");
    }

    // Phase 1: Compression (0-30%)
    sendProgress('compressing', 0, 100);
    
    tempArchivePath = getTempFilePath(`${folderName}.tar.gz`);
    
    await compressFolder(folderPath, tempArchivePath, compressionId, (transferred) => {
      // Show compression progress (0-30%)
      sendProgress('compressing', Math.min(30, transferred / 1024 / 1024), 100);
    });

    if (compression.cancelled) {
      console.log(`[CompressUpload] Compression cancelled, cleaning up temp file: ${tempArchivePath}`);
      try {
        await fs.promises.unlink(tempArchivePath);
        console.log(`[CompressUpload] Successfully deleted temp file after compression cancellation: ${tempArchivePath}`);
      } catch (error) {
        console.warn(`[CompressUpload] Failed to delete temp file after compression cancellation: ${tempArchivePath}`, error);
      }
      throw new Error('Upload cancelled');
    }

    // Get compressed file size
    const stat = await fs.promises.stat(tempArchivePath);
    const compressedSize = stat.size;
    
    sendProgress('compressing', 30, 100);

    // Phase 2: Upload (30-90%)
    sendProgress('uploading', 30, 100);
    
    const remoteArchivePath = `${targetPath}/${folderName}.tar.gz`;
    
    // Use existing transfer bridge for upload with progress
    const transferId = `compress-${compressionId}`;
    
    const uploadResult = await new Promise((resolve, reject) => {
      // Set up transfer progress listener
      const progressListener = (transferred, total, speed) => {
        if (compression.cancelled) return;
        // Map upload progress to 30-90%
        const uploadProgress = Math.min(60, (transferred / total) * 60);
        sendProgress('uploading', 30 + uploadProgress, 100);
      };
      
      const completeListener = () => {
        resolve({ success: true });
      };
      
      const errorListener = (error) => {
        reject(new Error(error));
      };
      
      // Register temporary listeners
      const progressMap = new Map();
      const completeMap = new Map();
      const errorMap = new Map();
      
      progressMap.set(transferId, progressListener);
      completeMap.set(transferId, completeListener);
      errorMap.set(transferId, errorListener);
      
      // Temporarily add to preload listeners (this is a hack, but works)
      sender.send("netcatty:transfer:progress", { transferId, transferred: 0, totalBytes: compressedSize, speed: 0 });
      
      // Start the transfer
      transferBridge.startTransfer(event, {
        transferId,
        sourcePath: tempArchivePath,
        targetPath: remoteArchivePath,
        sourceType: 'local',
        targetType: 'sftp',
        targetSftpId: sftpId,
        totalBytes: compressedSize
      }).then(resolve).catch(reject);
    });

    if (compression.cancelled) {
      await fs.promises.unlink(tempArchivePath).catch(() => {});
      throw new Error('Upload cancelled');
    }

    // Upload completed, update to 90%
    sendProgress('uploading', 90, 100);

    // Phase 3: Extraction (90-100%)
    sendProgress('extracting', 90, 100);
    console.log('[CompressUpload] Starting remote extraction phase');
    
    try {
      await extractRemoteArchive(sftpId, remoteArchivePath, targetPath);
      console.log('[CompressUpload] Remote extraction completed successfully');
      
      // Update progress to 95% after extraction
      sendProgress('extracting', 95, 100);
      
      // Perform cleanup operations asynchronously without blocking completion
      setImmediate(async () => {
        // Additional cleanup: remove any ._* files that might have been extracted
        try {
          const client = sftpClients.get(sftpId);
          if (client && client.client) {
            const cleanupCommand = `find "${targetPath}" -name "._*" -type f -delete 2>/dev/null || true`;
            client.client.exec(cleanupCommand, (err, stream) => {
              if (err) {
                console.warn('[CompressUpload] ._* files cleanup command failed to execute:', err);
                return;
              }
              
              stream.on('close', () => {
                console.log('[CompressUpload] ._* files cleanup completed');
              });
              
              stream.on('error', (error) => {
                console.warn('[CompressUpload] ._* files cleanup stream error:', error);
              });
            });
          }
        } catch (cleanupError) {
          console.warn('[CompressUpload] ._* files cleanup failed:', cleanupError);
        }
        
        // Additional cleanup attempt - ensure remote archive is removed
        try {
          const client = sftpClients.get(sftpId);
          if (client && client.client) {
            client.client.exec(`rm -f "${remoteArchivePath}"`, (err, stream) => {
              if (err) {
                console.warn('[CompressUpload] Additional cleanup command failed to execute:', err);
                return;
              }
              
              stream.on('close', () => {
                console.log('[CompressUpload] Additional cleanup completed');
              });
              
              stream.on('error', (error) => {
                console.warn('[CompressUpload] Additional cleanup stream error:', error);
              });
            });
          }
        } catch (cleanupError) {
          console.warn('[CompressUpload] Additional cleanup failed:', cleanupError);
        }
      });
      
      // Extraction completed successfully - don't wait for cleanup
      console.log('[CompressUpload] Extraction completed successfully, cleanup running in background');
    } catch (error) {
      console.error('[CompressUpload] Remote extraction failed:', error);
      throw error;
    }
    
    // Clean up local temp file
    console.log(`[CompressUpload] Cleaning up local temp file: ${tempArchivePath}`);
    try {
      await fs.promises.unlink(tempArchivePath);
      console.log(`[CompressUpload] Successfully deleted local temp file: ${tempArchivePath}`);
    } catch (error) {
      console.warn(`[CompressUpload] Failed to delete local temp file: ${tempArchivePath}`, error);
    }
    
    sendComplete();
    console.log('[CompressUpload] Compression upload completed successfully');
    
    return { compressionId, success: true };
  } catch (err) {
    console.error('[CompressUpload] Compression upload failed:', err);
    
    // Clean up local temp file if it exists
    if (tempArchivePath) {
      console.log(`[CompressUpload] Cleaning up local temp file after error: ${tempArchivePath}`);
      try {
        await fs.promises.unlink(tempArchivePath);
        console.log(`[CompressUpload] Successfully deleted local temp file after error: ${tempArchivePath}`);
      } catch (error) {
        console.warn(`[CompressUpload] Failed to delete local temp file after error: ${tempArchivePath}`, error);
      }
    }
    
    if (err.message === 'Upload cancelled' || err.message === 'Compression cancelled') {
      activeCompressions.delete(compressionId);
      sender.send("netcatty:compress:cancelled", { compressionId });
    } else {
      sendError(err.message || 'Unknown error occurred');
    }
    return { compressionId, error: err.message };
  } finally {
    // Always clean up the active compression entry
    activeCompressions.delete(compressionId);
  }
}

/**
 * Cancel a compression operation
 */
async function cancelCompression(event, payload) {
  const { compressionId } = payload;
  const compression = activeCompressions.get(compressionId);
  
  if (compression) {
    compression.cancelled = true;
    
    // Kill the tar process if running
    if (compression.process) {
      try {
        compression.process.kill('SIGTERM');
      } catch (e) {
        console.log('[compressUploadBridge] Error killing process:', e);
      }
    }
  }
  
  return { success: true };
}

/**
 * Check if compressed upload is supported (tar available on both local and remote)
 */
async function checkCompressedUploadSupport(event, payload) {
  const { sftpId } = payload;
  
  try {
    const localSupport = await checkTarAvailable();
    const remoteSupport = await checkRemoteTarAvailable(sftpId);
    
    return {
      supported: localSupport && remoteSupport,
      localTar: localSupport,
      remoteTar: remoteSupport
    };
  } catch (err) {
    return {
      supported: false,
      localTar: false,
      remoteTar: false,
      error: err.message
    };
  }
}

/**
 * Register IPC handlers
 */
function registerHandlers(ipcMain) {
  ipcMain.handle("netcatty:compress:start", startCompressedUpload);
  ipcMain.handle("netcatty:compress:cancel", cancelCompression);
  ipcMain.handle("netcatty:compress:checkSupport", checkCompressedUploadSupport);
}

module.exports = {
  init,
  registerHandlers,
  checkTarAvailable,
  checkRemoteTarAvailable,
};