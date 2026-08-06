import type { SftpFileEntry, SftpFilenameEncoding } from "../../../domain/models";
import {
  accountSftpDirectoryEntries,
  claimSftpDirectoryVisit,
  compareDirectoryTraversalPaths,
  createSftpDirectoryBranchAncestors,
  createSftpDirectoryTraversalBudget,
  releaseSftpDirectoryVisit,
  shouldFollowSftpSymlinkDirectory,
  type SftpDirectoryTraversalBudget,
} from "../../../domain/sftpDirectoryCheckpoint";
import { netcattyBridge } from "../../../infrastructure/services/netcattyBridge";
import { logger } from "../../../lib/logger";
import {
  DEFAULT_SFTP_DIRECTORY_LISTING_CONCURRENCY,
  resolveSftpDirectoryListingConcurrency,
  runBoundedConcurrency,
} from "./transferConcurrency";
import { joinPath, joinTransferTargetPath } from "./utils";

export type DiscoveredTransferFile = {
  name: string;
  sourcePath: string;
  targetPath: string;
  size: number;
  lastModified: number;
  /**
   * True when the listing entry was a symlink. Link-node size/mtime must not
   * drive skip-unchanged; transfer follows the target bytes.
   */
  isSymlink?: boolean;
};

export type DiscoveredTransferDirectory = {
  sourcePath: string;
  targetPath: string;
};

export type DiscoverTransferTreeResult = {
  files: DiscoveredTransferFile[];
  directories: DiscoveredTransferDirectory[];
  /** Symlink directories omitted at max follow depth (incomplete tree). */
  omittedSymlinkDirectoryErrors: number;
};

type ListingGate = {
  run: <T>(fn: () => Promise<T>) => Promise<T>;
};

function createListingGate(concurrency = DEFAULT_SFTP_DIRECTORY_LISTING_CONCURRENCY): ListingGate {
  const limit = Math.max(1, Math.floor(concurrency) || 1);
  let active = 0;
  const waiters: Array<() => void> = [];
  const acquire = async () => {
    if (active < limit) {
      active += 1;
      return;
    }
    await new Promise<void>((resolve) => {
      waiters.push(() => {
        active += 1;
        resolve();
      });
    });
  };
  const release = () => {
    active = Math.max(0, active - 1);
    const next = waiters.shift();
    if (next) next();
  };
  return {
    async run<T>(fn: () => Promise<T>): Promise<T> {
      await acquire();
      try {
        return await fn();
      } finally {
        release();
      }
    },
  };
}

function getEntrySize(entry: SftpFileEntry): number {
  if (typeof entry.size === "string") {
    const parsed = parseInt(entry.size, 10);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
  }
  return typeof entry.size === "number" && entry.size > 0 ? entry.size : 0;
}

export type DiscoverTransferTreeOptions = {
  sourcePath: string;
  targetPath: string;
  sourceIsLocal: boolean;
  sourceSftpId: string | null;
  sourceEncoding: SftpFilenameEncoding;
  followSymlinks?: boolean;
  listLocalFiles: (path: string) => Promise<SftpFileEntry[]>;
  listRemoteFiles: (sftpId: string, path: string, encoding?: SftpFilenameEncoding) => Promise<SftpFileEntry[]>;
  shouldAbort?: () => boolean;
  /** Honor transfer-center pause between BFS waves / listings. */
  waitWhilePaused?: () => Promise<void>;
  onDiscoveredFiles?: (totalFiles: number) => void;
  listingConcurrency?: number;
  traversalBudget?: SftpDirectoryTraversalBudget;
};

/**
 * Full-tree discovery (rsync --no-inc-recursive style): walk every directory
 * before transfer starts so the UI can show a stable file total.
 * Listings are bounded-parallel; nested walks share one semaphore.
 */
export async function discoverTransferTree(
  options: DiscoverTransferTreeOptions,
): Promise<DiscoverTransferTreeResult> {
  const followSymlinks = options.followSymlinks === true;
  const listingConcurrency = options.listingConcurrency
    ?? resolveSftpDirectoryListingConcurrency();
  const listingGate = createListingGate(listingConcurrency);
  const traversal = options.traversalBudget ?? createSftpDirectoryTraversalBudget();
  const files: DiscoveredTransferFile[] = [];
  const directories: DiscoveredTransferDirectory[] = [
    { sourcePath: options.sourcePath, targetPath: options.targetPath },
  ];
  let omittedSymlinkDirectoryErrors = 0;

  type DirJob = {
    sourcePath: string;
    targetPath: string;
    symlinkDepth: number;
    /** Ancestors on this BFS branch (copied for parallel sibling aliases). */
    branchAncestors: Set<string>;
    /** False for the root (already in `directories`); true for accepted children. */
    includeInCreationPlan: boolean;
  };

  const queue: DirJob[] = [{
    sourcePath: options.sourcePath,
    targetPath: options.targetPath,
    symlinkDepth: 0,
    branchAncestors: createSftpDirectoryBranchAncestors(),
    includeInCreationPlan: false,
  }];

  const publishCount = () => {
    options.onDiscoveredFiles?.(files.length);
  };

  const processDir = async (job: DirJob): Promise<DirJob[]> => {
    if (options.shouldAbort?.()) throw new Error("Transfer cancelled");
    await options.waitWhilePaused?.();
    if (options.shouldAbort?.()) throw new Error("Transfer cancelled");
    let claimedCanonicalPath: string | null = null;
    try {
      let canonicalPath = job.sourcePath;
      if (!options.sourceIsLocal && options.sourceSftpId) {
        const bridge = netcattyBridge.get();
        canonicalPath = await bridge?.realpathSftp?.(
          options.sourceSftpId,
          job.sourcePath,
          options.sourceEncoding,
        ).catch(() => job.sourcePath) ?? job.sourcePath;
      }
      // Bound both remote and local pre-scan trees (Codex P1 on 86c64f48).
      claimedCanonicalPath = claimSftpDirectoryVisit(
        traversal,
        canonicalPath,
        job.branchAncestors,
      );
      // Cycle / already visited: do not create an empty target dir for this path.
      if (!claimedCanonicalPath) return [];

      // Only record directories after the visit is accepted so cyclic symlink
      // children (e.g. /source/loop -> /source) are omitted from mkdir plans.
      if (job.includeInCreationPlan) {
        directories.push({ sourcePath: job.sourcePath, targetPath: job.targetPath });
      }

      const listed = await listingGate.run(async () => {
        if (options.shouldAbort?.()) throw new Error("Transfer cancelled");
        await options.waitWhilePaused?.();
        if (options.shouldAbort?.()) throw new Error("Transfer cancelled");
        if (options.sourceIsLocal) {
          return options.listLocalFiles(job.sourcePath);
        }
        if (!options.sourceSftpId) throw new Error("No source connection");
        return options.listRemoteFiles(
          options.sourceSftpId,
          job.sourcePath,
          options.sourceEncoding,
        );
      });

      const filtered = listed.filter((entry) => entry.name !== "." && entry.name !== "..");
      accountSftpDirectoryEntries(traversal, filtered.length);

      const childDirs: DirJob[] = [];
      const regularFiles: SftpFileEntry[] = [];
      for (const entry of filtered) {
        if (entry.type === "directory") {
          childDirs.push({
            sourcePath: joinPath(job.sourcePath, entry.name),
            targetPath: joinTransferTargetPath(job.targetPath, entry.name),
            symlinkDepth: job.symlinkDepth,
            branchAncestors: createSftpDirectoryBranchAncestors(job.branchAncestors),
            includeInCreationPlan: true,
          });
        } else if (
          followSymlinks
          && entry.type === "symlink"
          && entry.linkTarget === "directory"
        ) {
          if (shouldFollowSftpSymlinkDirectory(job.symlinkDepth)) {
            childDirs.push({
              sourcePath: joinPath(job.sourcePath, entry.name),
              targetPath: joinTransferTargetPath(job.targetPath, entry.name),
              symlinkDepth: job.symlinkDepth + 1,
              branchAncestors: createSftpDirectoryBranchAncestors(job.branchAncestors),
              includeInCreationPlan: true,
            });
          } else {
            omittedSymlinkDirectoryErrors += 1;
            logger.warn(
              `[SFTP] Skipping symlink directory at max depth during pre-scan: ${joinPath(job.sourcePath, entry.name)}`,
            );
          }
        } else {
          regularFiles.push(entry);
        }
      }

      childDirs.sort((left, right) => left.sourcePath.localeCompare(right.sourcePath));
      regularFiles.sort((left, right) => left.name.localeCompare(right.name));

      for (const entry of regularFiles) {
        files.push({
          name: entry.name,
          sourcePath: joinPath(job.sourcePath, entry.name),
          targetPath: joinTransferTargetPath(job.targetPath, entry.name),
          size: getEntrySize(entry),
          lastModified: typeof entry.lastModified === "number" ? entry.lastModified : 0,
          isSymlink: entry.type === "symlink",
        });
      }
      publishCount();
      return childDirs;
    } finally {
      if (claimedCanonicalPath) {
        releaseSftpDirectoryVisit(traversal, claimedCanonicalPath, job.branchAncestors);
      }
    }
  };

  // BFS waves: each wave lists up to listingConcurrency directories in parallel.
  while (queue.length > 0) {
    if (options.shouldAbort?.()) throw new Error("Transfer cancelled");
    await options.waitWhilePaused?.();
    if (options.shouldAbort?.()) throw new Error("Transfer cancelled");
    const wave = queue.splice(0, queue.length);
    const nested: DirJob[][] = new Array(wave.length);
    await runBoundedConcurrency(
      wave,
      listingConcurrency,
      async (job, index) => {
        nested[index] = await processDir(job);
      },
    );
    for (const children of nested) {
      if (!children) continue;
      for (const child of children) queue.push(child);
    }
  }

  // Match dedicated-resume / transfer-center ordering so directoryEntryIndex and
  // manifest checkpoints stay valid across interrupt + rebuild.
  files.sort((left, right) => compareDirectoryTraversalPaths(left.sourcePath, right.sourcePath));

  return { files, directories, omittedSymlinkDirectoryErrors };
}
