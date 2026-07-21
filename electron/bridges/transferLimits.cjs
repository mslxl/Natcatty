"use strict";

// Keep ssh2's default 32KB request size. Some SFTP servers mishandle larger
// requests and can silently produce truncated/corrupt files (GitHub #2022).
const TRANSFER_CHUNK_SIZE = 32 * 1024;

// Upload fanout stays conservative so transfers do not monopolize the shared
// SSH/network path used by interactive terminals (GitHub #1507).
const UPLOAD_TRANSFER_CONCURRENCY = 4;

// Downloads need a larger request window on high-latency proxy paths. 64 is
// ssh2's fastGet default and, with the safe 32KB request size, restores the 2MB
// in-flight window Netcatty used before the shared chunk-size fix in #2030.
const DOWNLOAD_TRANSFER_CONCURRENCY = 64;
const FAST_DOWNLOAD_CHANNELS_PER_SESSION = 1;

module.exports = {
  DOWNLOAD_TRANSFER_CONCURRENCY,
  FAST_DOWNLOAD_CHANNELS_PER_SESSION,
  TRANSFER_CHUNK_SIZE,
  UPLOAD_TRANSFER_CONCURRENCY,
};
