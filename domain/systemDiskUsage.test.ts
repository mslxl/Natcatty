import assert from "node:assert/strict";
import test from "node:test";

import { aggregateMountedDiskUsage } from "./systemDiskUsage.ts";

test("aggregateMountedDiskUsage totals every mounted disk", () => {
  assert.deepEqual(
    aggregateMountedDiskUsage([
      { mountPoint: "/", used: 20, total: 100 },
      { mountPoint: "/data", used: 60, total: 300 },
    ]),
    { used: 80, total: 400, percent: 20 },
  );
});

test("aggregateMountedDiskUsage skips rclone/CloudDrive/ufs network FUSE capacities", () => {
  assert.deepEqual(
    aggregateMountedDiskUsage([
      { capacityKey: "/dev/sda1", mountPoint: "/", used: 20, total: 100 },
      { capacityKey: "/dev/sdb1", mountPoint: "/data", used: 10, total: 50 },
      { capacityKey: "fuse.rclone", mountPoint: "/mnt/rclone", used: 500, total: 1000 },
      { capacityKey: "rclone:gdrive:media", mountPoint: "/mnt/gdrive", used: 1000, total: 2000 },
      { capacityKey: "CloudDrive", mountPoint: "/CloudNAS/CloudDrive", used: 2000, total: 4000 },
      { capacityKey: "ufs", mountPoint: "/mnt/ufs", used: 500, total: 1000 },
      { capacityKey: "gdrive:media", filesystemType: "fuse.rclone", mountPoint: "/mnt/remote", used: 800, total: 1600 },
    ]),
    { used: 30, total: 150, percent: 20 },
  );
});

test("aggregateMountedDiskUsage trusts a reported local filesystem type over its source name", () => {
  assert.deepEqual(
    aggregateMountedDiskUsage([
      { capacityKey: "ufs", filesystemType: "ext4", mountPoint: "/data", used: 5, total: 10 },
      { capacityKey: "remote:gdrive", filesystemType: "fuse.rclone", mountPoint: "/mnt/gdrive", used: 50, total: 100 },
    ]),
    { used: 5, total: 10, percent: 50 },
  );
});

test("aggregateMountedDiskUsage keeps a local UFS filesystem when its type is reported", () => {
  assert.deepEqual(
    aggregateMountedDiskUsage([
      { capacityKey: "ufs", filesystemType: "ufs", mountPoint: "/data", used: 5, total: 10 },
      { capacityKey: "remote:gdrive", filesystemType: "fuse.rclone", mountPoint: "/mnt/gdrive", used: 50, total: 100 },
    ]),
    { used: 5, total: 10, percent: 50 },
  );
});

test("aggregateMountedDiskUsage uses source heuristics when filesystem type is unavailable", () => {
  assert.equal(
    aggregateMountedDiskUsage([
      { capacityKey: "rclone:gdrive", filesystemType: "-", mountPoint: "/mnt/gdrive", used: 50, total: 100 },
    ]),
    null,
  );
});

test("aggregateMountedDiskUsage ignores unusable disk rows", () => {
  assert.deepEqual(
    aggregateMountedDiskUsage([
      { mountPoint: "/", used: 25, total: 100 },
      { mountPoint: "/missing", used: Number.NaN, total: 20 },
      { mountPoint: "/zero", used: 0, total: 0 },
      { mountPoint: "/invalid", used: -1, total: 20 },
    ]),
    { used: 25, total: 100, percent: 25 },
  );
  assert.equal(aggregateMountedDiskUsage([]), null);
});

test("aggregateMountedDiskUsage counts a repeated capacity group only once", () => {
  assert.deepEqual(
    aggregateMountedDiskUsage([
      { capacityKey: "/dev/sda1", mountPoint: "/", used: 20, total: 100 },
      { capacityKey: "/dev/sda1", mountPoint: "/bind-root", used: 20, total: 100 },
      { capacityKey: "/dev/sdb1", mountPoint: "/data", used: 60, total: 300 },
    ]),
    { used: 80, total: 400, percent: 20 },
  );
});

test("aggregateMountedDiskUsage preserves fractional capacity until display formatting", () => {
  assert.deepEqual(
    aggregateMountedDiskUsage([
      { capacityKey: "/dev/sda1", mountPoint: "/", used: 0.125, total: 0.5 },
      { capacityKey: "/dev/sdb1", mountPoint: "/data", used: 0.25, total: 1.5 },
    ]),
    { used: 0.375, total: 2, percent: 18.75 },
  );
});

test("aggregateMountedDiskUsage counts shared APFS container capacity once", () => {
  assert.deepEqual(
    aggregateMountedDiskUsage([
      { capacityKey: "apfs:/dev/disk3", mountPoint: "/Volumes/One", used: 40, total: 500 },
      { capacityKey: "apfs:/dev/disk3", mountPoint: "/Volumes/Two", used: 90, total: 500 },
    ]),
    { used: 90, total: 500, percent: 18 },
  );
});

test("aggregateMountedDiskUsage includes an overfull filesystem", () => {
  assert.deepEqual(
    aggregateMountedDiskUsage([
      { capacityKey: "/dev/sda1", mountPoint: "/", used: 110, total: 100 },
      { capacityKey: "/dev/sdb1", mountPoint: "/data", used: 25, total: 100 },
    ]),
    { used: 135, total: 200, percent: 67.5 },
  );
  assert.deepEqual(
    aggregateMountedDiskUsage([
      { capacityKey: "/dev/sda1", mountPoint: "/", used: 110, total: 100 },
    ]),
    { used: 110, total: 100, percent: 100 },
  );
});
