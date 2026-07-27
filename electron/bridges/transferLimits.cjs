"use strict";

// Keep ssh2's default 32KB request size. Some SFTP servers mishandle larger
// requests and can silently produce truncated/corrupt files (GitHub #2022).
const TRANSFER_CHUNK_SIZE = 32 * 1024;

// Upload fanout: 64 parallel 32KB WRITE requests (~2MB in flight). Matches
// Electerm transfer.js / ssh2-style defaults (concurrency 64, chunk 32KB) for
// high-RTT body feel after #2449. Chunk size stays 32KB for server compatibility
// (#2022 / #2030). Uploads still prefer an isolated SFTP channel / dedicated
// transfer session so interactive terminal traffic is not starved (#1507).
const UPLOAD_TRANSFER_CONCURRENCY = 64;

// Downloads need a larger request window on high-latency proxy paths. 64 is
// ssh2's fastGet default and, with the safe 32KB request size, restores the 2MB
// in-flight window Netcatty used before the shared chunk-size fix in #2030.
const DOWNLOAD_TRANSFER_CONCURRENCY = 64;
// Only one file per SFTP session gets the 64-request fast path. Concurrent
// files keep moving through the compatible stream path instead of multiplying
// fastGet pressure. Folder fan-out is capped in the renderer by
// runSftpTransferWorkers (settings transfer concurrency); multi-select
// top-level files are not throttled by that setting.
const FAST_DOWNLOAD_CHANNELS_PER_SESSION = 1;

module.exports = {
  DOWNLOAD_TRANSFER_CONCURRENCY,
  FAST_DOWNLOAD_CHANNELS_PER_SESSION,
  TRANSFER_CHUNK_SIZE,
  UPLOAD_TRANSFER_CONCURRENCY,
};
