/**
 * Transfer Bridge - Handles file transfers with progress and cancellation
 * Extracted from main.cjs for single responsibility
 */

const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");
const { encodePathForSession, ensureRemoteDirForSession } = require("./sftpBridge.cjs");

// Shared references
let sftpClients = null;
let electronModule = null;

// Active transfers storage
const activeTransfers = new Map();

/**
 * Initialize the transfer bridge with dependencies
 */
function init(deps) {
  sftpClients = deps.sftpClients;
  electronModule = deps.electronModule;
}

/**
 * Start a file transfer
 */
async function startTransfer(event, payload) {
  const {
    transferId,
    sourcePath,
    targetPath,
    sourceType,
    targetType,
    sourceSftpId,
    targetSftpId,
    totalBytes,
    sourceEncoding,
    targetEncoding,
  } = payload;
  const sender = event.sender;
  
  // Register transfer for cancellation
  activeTransfers.set(transferId, { cancelled: false });
  
  let lastTime = Date.now();
  let lastTransferred = 0;
  let speed = 0;
  
  const sendProgress = (transferred, total) => {
    if (activeTransfers.get(transferId)?.cancelled) return;
    
    const now = Date.now();
    const elapsed = now - lastTime;
    if (elapsed >= 100) {
      speed = Math.round((transferred - lastTransferred) / (elapsed / 1000));
      lastTime = now;
      lastTransferred = transferred;
    }
    
    sender.send("netcatty:transfer:progress", { transferId, transferred, speed, totalBytes: total });
  };
  
  const sendComplete = () => {
    activeTransfers.delete(transferId);
    sender.send("netcatty:transfer:complete", { transferId });
  };
  
  const sendError = (error) => {
    activeTransfers.delete(transferId);
    sender.send("netcatty:transfer:error", { transferId, error: error.message || String(error) });
  };
  
  const isCancelled = () => activeTransfers.get(transferId)?.cancelled;
  
  try {
    let fileSize = totalBytes || 0;
    
    // Get file size if not provided
    if (!fileSize) {
      if (sourceType === 'local') {
        const stat = await fs.promises.stat(sourcePath);
        fileSize = stat.size;
      } else if (sourceType === 'sftp') {
        const client = sftpClients.get(sourceSftpId);
        if (!client) throw new Error("Source SFTP session not found");
        const encodedSourcePath = encodePathForSession(sourceSftpId, sourcePath, sourceEncoding);
        const stat = await client.stat(encodedSourcePath);
        fileSize = stat.size;
      }
    }
    
    // Send initial progress
    sendProgress(0, fileSize);
    
    // Handle different transfer scenarios
    if (sourceType === 'local' && targetType === 'sftp') {
      // Upload: Local -> SFTP
      const client = sftpClients.get(targetSftpId);
      if (!client) throw new Error("Target SFTP session not found");
      
      const dir = path.dirname(targetPath).replace(/\\/g, '/');
      try { await ensureRemoteDirForSession(targetSftpId, dir, targetEncoding); } catch {}
      
      const encodedTargetPath = encodePathForSession(targetSftpId, targetPath, targetEncoding);
      await client.fastPut(sourcePath, encodedTargetPath, {
        step: (totalTransferred, chunk, total) => {
          if (isCancelled()) {
            throw new Error('Transfer cancelled');
          }
          sendProgress(totalTransferred, total);
        }
      });
      
    } else if (sourceType === 'sftp' && targetType === 'local') {
      // Download: SFTP -> Local
      const client = sftpClients.get(sourceSftpId);
      if (!client) throw new Error("Source SFTP session not found");
      
      const dir = path.dirname(targetPath);
      await fs.promises.mkdir(dir, { recursive: true });
      
      const encodedSourcePath = encodePathForSession(sourceSftpId, sourcePath, sourceEncoding);
      await client.fastGet(encodedSourcePath, targetPath, {
        step: (totalTransferred, chunk, total) => {
          if (isCancelled()) {
            throw new Error('Transfer cancelled');
          }
          sendProgress(totalTransferred, total);
        }
      });
      
    } else if (sourceType === 'local' && targetType === 'local') {
      // Local copy: use streams
      const dir = path.dirname(targetPath);
      await fs.promises.mkdir(dir, { recursive: true });
      
      await new Promise((resolve, reject) => {
        const readStream = fs.createReadStream(sourcePath);
        const writeStream = fs.createWriteStream(targetPath);
        let transferred = 0;
        
        const transfer = activeTransfers.get(transferId);
        if (transfer) {
          transfer.readStream = readStream;
          transfer.writeStream = writeStream;
        }
        
        readStream.on('data', (chunk) => {
          if (isCancelled()) {
            readStream.destroy();
            writeStream.destroy();
            reject(new Error('Transfer cancelled'));
            return;
          }
          transferred += chunk.length;
          sendProgress(transferred, fileSize);
        });
        
        readStream.on('error', reject);
        writeStream.on('error', reject);
        writeStream.on('finish', resolve);
        
        readStream.pipe(writeStream);
      });
      
    } else if (sourceType === 'sftp' && targetType === 'sftp') {
      // SFTP to SFTP: download to temp then upload
      const tempPath = path.join(os.tmpdir(), `netcatty-transfer-${transferId}`);
      
      const sourceClient = sftpClients.get(sourceSftpId);
      const targetClient = sftpClients.get(targetSftpId);
      if (!sourceClient) throw new Error("Source SFTP session not found");
      if (!targetClient) throw new Error("Target SFTP session not found");
      
      // Download phase (0-50%)
      const encodedSourcePath = encodePathForSession(sourceSftpId, sourcePath, sourceEncoding);
      await sourceClient.fastGet(encodedSourcePath, tempPath, {
        step: (totalTransferred, chunk, total) => {
          if (isCancelled()) {
            throw new Error('Transfer cancelled');
          }
          sendProgress(Math.floor(totalTransferred / 2), fileSize);
        }
      });
      
      if (isCancelled()) {
        try { await fs.promises.unlink(tempPath); } catch {}
        throw new Error('Transfer cancelled');
      }
      
      // Upload phase (50-100%)
      const dir = path.dirname(targetPath).replace(/\\/g, '/');
      try { await ensureRemoteDirForSession(targetSftpId, dir, targetEncoding); } catch {}
      
      const encodedTargetPath = encodePathForSession(targetSftpId, targetPath, targetEncoding);
      await targetClient.fastPut(tempPath, encodedTargetPath, {
        step: (totalTransferred, chunk, total) => {
          if (isCancelled()) {
            throw new Error('Transfer cancelled');
          }
          sendProgress(Math.floor(fileSize / 2) + Math.floor(totalTransferred / 2), fileSize);
        }
      });
      
      // Cleanup temp file
      try { await fs.promises.unlink(tempPath); } catch {}
      
    } else {
      throw new Error("Invalid transfer configuration");
    }
    
    // Send final 100% progress
    sendProgress(fileSize, fileSize);
    sendComplete();
    
    return { transferId, totalBytes: fileSize };
  } catch (err) {
    if (err.message === 'Transfer cancelled') {
      activeTransfers.delete(transferId);
      sender.send("netcatty:transfer:cancelled", { transferId });
    } else {
      sendError(err);
    }
    return { transferId, error: err.message };
  }
}

/**
 * Cancel a transfer
 */
async function cancelTransfer(event, payload) {
  const { transferId } = payload;
  const transfer = activeTransfers.get(transferId);
  if (transfer) {
    transfer.cancelled = true;
    if (transfer.readStream) {
      try { transfer.readStream.destroy(); } catch {}
    }
    if (transfer.writeStream) {
      try { transfer.writeStream.destroy(); } catch {}
    }
    activeTransfers.delete(transferId);
  }
  return { success: true };
}

/**
 * Register IPC handlers for transfer operations
 */
function registerHandlers(ipcMain) {
  ipcMain.handle("netcatty:transfer:start", startTransfer);
  ipcMain.handle("netcatty:transfer:cancel", cancelTransfer);
}

module.exports = {
  init,
  registerHandlers,
  startTransfer,
  cancelTransfer,
};
