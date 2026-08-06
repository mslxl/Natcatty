/* eslint-disable no-undef */

"use strict";

const NVIDIA_GPU_QUERY = [
  "nvidia-smi",
  "--query-gpu=index,uuid,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,power.limit,fan.speed,driver_version",
  "--format=csv,noheader,nounits",
].join(" ");

const NVIDIA_PROCESS_QUERY = [
  "nvidia-smi",
  "--query-compute-apps=gpu_uuid,pid,process_name,used_gpu_memory",
  "--format=csv,noheader,nounits",
].join(" ");

/**
 * Local Windows collector — PowerShell (execOnLocalMachine uses powershell.exe).
 * Ascend on Windows is uncommon; still attempt npu-smi when present.
 */
const ACCELERATOR_COLLECT_SCRIPT_WINDOWS = [
  'Write-Output "__NC_ACCEL_BEGIN__"; ',
  "if (Get-Command nvidia-smi -ErrorAction SilentlyContinue) { ",
  'Write-Output "__NC_NVIDIA_DEVICES__"; ',
  `${NVIDIA_GPU_QUERY} 2>$null; `,
  'Write-Output "__NC_NVIDIA_PROCESSES__"; ',
  `${NVIDIA_PROCESS_QUERY} 2>$null; `,
  "}; ",
  "if (Get-Command npu-smi -ErrorAction SilentlyContinue) { ",
  'Write-Output "__NC_NPU_BEGIN__"; ',
  'Write-Output "__NC_NPU_INFO__"; ',
  "npu-smi info 2>$null; ",
  'Write-Output "__NC_NPU_PROCS__"; ',
  "npu-smi info -t proc-mem 2>$null; ",
  'Write-Output "__NC_NPU_END__"; ',
  "}; ",
  'Write-Output "__NC_ACCEL_END__"',
].join("");

/**
 * Remote collector body (no outer quoting). Wrapped with JSON.stringify so
 * nested sed single-quotes cannot break `sh -c`.
 */
const ACCELERATOR_COLLECT_INNER = [
  'printf "%s\\n" "__NC_ACCEL_BEGIN__"; ',
  'if command -v nvidia-smi >/dev/null 2>&1; then ',
  'printf "%s\\n" "__NC_NVIDIA_DEVICES__"; ',
  `${NVIDIA_GPU_QUERY} 2>/dev/null || true; `,
  'printf "%s\\n" "__NC_NVIDIA_PROCESSES__"; ',
  `${NVIDIA_PROCESS_QUERY} 2>/dev/null || true; `,
  "fi; ",
  'if command -v npu-smi >/dev/null 2>&1; then ',
  'printf "%s\\n" "__NC_NPU_BEGIN__"; ',
  "ids=$(npu-smi info -l 2>/dev/null | sed -n 's/^[[:space:]]*NPU ID[[:space:]]*:[[:space:]]*\\([0-9][0-9]*\\).*/\\1/p'); ",
  'if [ -n "$ids" ]; then ',
  'for id in $ids; do ',
  'printf "%s\\n" "__NC_NPU_DEVICE__=$id"; ',
  'npu-smi info -t board -i "$id" 2>/dev/null || true; ',
  'npu-smi info -t common -i "$id" 2>/dev/null || true; ',
  'npu-smi info -t usages -i "$id" 2>/dev/null || true; ',
  'npu-smi info -t memory -i "$id" 2>/dev/null || true; ',
  "done; ",
  "else ",
  'printf "%s\\n" "__NC_NPU_INFO__"; ',
  "npu-smi info 2>/dev/null || true; ",
  "fi; ",
  'printf "%s\\n" "__NC_NPU_PROCS__"; ',
  "npu-smi info -t proc-mem 2>/dev/null || true; ",
  'printf "%s\\n" "__NC_NPU_END__"; ',
  "fi; ",
  'printf "%s\\n" "__NC_ACCEL_END__"',
].join("");

const ACCELERATOR_COLLECT_SCRIPT = `exec sh -c ${JSON.stringify(ACCELERATOR_COLLECT_INNER)}`;

function parseCsvNumber(raw) {
  const text = String(raw ?? "").trim();
  if (!text || /^\[?n\/?a\]?$/i.test(text) || /^not\s+supported$/i.test(text)) return null;
  const n = Number.parseFloat(text.replace(/[,%]/g, ""));
  return Number.isFinite(n) ? n : null;
}

function parseCsvFields(line) {
  // nvidia-smi csv is simple (no embedded commas in queried fields with nounits)
  return String(line || "")
    .split(",")
    .map((part) => part.trim());
}

function parseNvidiaDevices(sectionText) {
  const devices = [];
  for (const line of String(sectionText || "").split("\n")) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("__NC_")) continue;
    const fields = parseCsvFields(trimmed);
    if (fields.length < 3) continue;
    const index = Number.parseInt(fields[0], 10);
    if (!Number.isFinite(index)) continue;
    devices.push({
      vendor: "nvidia",
      index,
      uuid: fields[1] || "",
      name: fields[2] || `GPU ${index}`,
      utilizationPercent: parseCsvNumber(fields[3]),
      memoryUsedMb: parseCsvNumber(fields[4]),
      memoryTotalMb: parseCsvNumber(fields[5]),
      temperatureC: parseCsvNumber(fields[6]),
      powerDrawW: parseCsvNumber(fields[7]),
      powerLimitW: parseCsvNumber(fields[8]),
      fanPercent: parseCsvNumber(fields[9]),
      driverVersion: fields[10] || null,
      health: null,
    });
  }
  return devices;
}

function parseNvidiaProcesses(sectionText, devices) {
  const uuidToIndex = new Map();
  for (const device of devices) {
    if (device.uuid) uuidToIndex.set(device.uuid, device.index);
  }
  const processes = [];
  for (const line of String(sectionText || "").split("\n")) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("__NC_")) continue;
    const fields = parseCsvFields(trimmed);
    if (fields.length < 3) continue;
    const uuid = fields[0] || "";
    const pid = Number.parseInt(fields[1], 10);
    if (!Number.isFinite(pid) || pid <= 0) continue;
    processes.push({
      vendor: "nvidia",
      gpuIndex: uuidToIndex.has(uuid) ? uuidToIndex.get(uuid) : 0,
      pid,
      processName: fields[2] || "",
      memoryUsedMb: parseCsvNumber(fields[3]),
    });
  }
  return processes;
}

function extractKvNumber(block, labels) {
  for (const label of labels) {
    const re = new RegExp(`${label}\\s*[:=]\\s*([0-9]+(?:\\.[0-9]+)?)`, "i");
    const match = String(block || "").match(re);
    if (match) {
      const n = Number.parseFloat(match[1]);
      if (Number.isFinite(n)) return n;
    }
  }
  return null;
}

function extractKvText(block, labels) {
  for (const label of labels) {
    const re = new RegExp(`${label}\\s*[:=]\\s*(.+)`, "i");
    const match = String(block || "").match(re);
    if (match) {
      const value = match[1].trim().replace(/\s{2,}.*/, "").trim();
      if (value) return value;
    }
  }
  return null;
}

function parseAscendDeviceBlock(index, block) {
  const name =
    extractKvText(block, [
      "Product Name",
      "NPU Name",
      "Chip Name",
      "Model",
      "Board Name",
    ]) || `Ascend NPU ${index}`;
  const utilizationPercent = extractKvNumber(block, [
    "Aicore Usage Rate\\(%\\)",
    "AICore Usage Rate\\(%\\)",
    "Aicore Usage Rate",
    "AI Core Usage",
  ]);
  // Absolute MB fields only — never treat "HBM Usage Rate(%)" as megabytes.
  const hbmUsed = extractKvNumber(block, [
    "HBM Used Memory\\(MB\\)",
    "HBM Memory Usage\\(MB\\)",
    "Used HBM Memory\\(MB\\)",
    "Used HBM Memory",
  ]);
  const hbmTotal = extractKvNumber(block, [
    "HBM Total Memory\\(MB\\)",
    "HBM Capacity\\(MB\\)",
    "Total HBM Memory\\(MB\\)",
    "Total HBM Memory",
  ]);
  const hbmUsageRate = extractKvNumber(block, [
    "HBM Usage Rate\\(%\\)",
    "HBM Usage Rate",
  ]);
  // memory command sometimes reports "Used / Total"
  const hbmPair = String(block || "").match(
    /HBM[^\n]*?(?:Memory|Usage)\([^\n]*?(\d+(?:\.\d+)?)\s*\/\s*(\d+(?:\.\d+)?)/i,
  ) || String(block || "").match(
    /HBM Used Memory[^\n]*?(\d+(?:\.\d+)?)\s*\/\s*(\d+(?:\.\d+)?)/i,
  );
  let memoryUsedMb = hbmPair ? Number.parseFloat(hbmPair[1]) : hbmUsed;
  let memoryTotalMb = hbmPair ? Number.parseFloat(hbmPair[2]) : hbmTotal;
  // Derive used MB from rate only when total is known and used is missing.
  if (
    !Number.isFinite(memoryUsedMb)
    && Number.isFinite(hbmUsageRate)
    && Number.isFinite(memoryTotalMb)
    && memoryTotalMb > 0
  ) {
    memoryUsedMb = (hbmUsageRate / 100) * memoryTotalMb;
  }
  const temperatureC = extractKvNumber(block, [
    "Temperature\\(C\\)",
    "Temp\\(C\\)",
    "Temperature",
  ]);
  const powerDrawW = extractKvNumber(block, [
    "NPU Real-time Power\\(W\\)",
    "Power Dissipation\\(W\\)",
    "Power\\(W\\)",
    "Power",
  ]);
  const health = extractKvText(block, ["Health", "Health Status"]);

  return {
    vendor: "ascend",
    index,
    uuid: "",
    name,
    utilizationPercent: Number.isFinite(utilizationPercent) ? utilizationPercent : null,
    memoryUsedMb: Number.isFinite(memoryUsedMb) ? memoryUsedMb : null,
    memoryTotalMb: Number.isFinite(memoryTotalMb) ? memoryTotalMb : null,
    temperatureC: Number.isFinite(temperatureC) ? temperatureC : null,
    powerDrawW: Number.isFinite(powerDrawW) ? powerDrawW : null,
    powerLimitW: null,
    fanPercent: null,
    driverVersion: null,
    health,
  };
}

function parseAscendInfoTable(sectionText) {
  const devices = [];
  const lines = String(sectionText || "").split("\n");
  for (const line of lines) {
    // | 0     910B3                   | OK              | 71.8       42                             |
    const match = line.match(
      /^\|\s*(\d+)\s+(\S+(?:\s+\S+)*?)\s+\|\s*(\S+)\s+\|\s*([0-9.]+)\s+([0-9.]+)\s*\|/,
    );
    if (!match) continue;
    const index = Number.parseInt(match[1], 10);
    if (!Number.isFinite(index)) continue;
    if (devices.some((d) => d.index === index)) continue;
    devices.push({
      vendor: "ascend",
      index,
      uuid: "",
      name: match[2].trim(),
      utilizationPercent: null,
      memoryUsedMb: null,
      memoryTotalMb: null,
      temperatureC: parseCsvNumber(match[5]),
      powerDrawW: parseCsvNumber(match[4]),
      powerLimitW: null,
      fanPercent: null,
      driverVersion: null,
      health: match[3].trim(),
    });
  }

  // Chip rows: | 0      0        0               12          1234 / 32768        0 / 32768     |
  for (const line of lines) {
    const chip = line.match(
      /^\|\s*(\d+)\s+\d+\s+\d+\s+(\d+(?:\.\d+)?)\s+(\d+(?:\.\d+)?)\s*\/\s*(\d+(?:\.\d+)?)\s+(\d+(?:\.\d+)?)\s*\/\s*(\d+(?:\.\d+)?)/,
    );
    if (!chip) continue;
    const index = Number.parseInt(chip[1], 10);
    const device = devices.find((d) => d.index === index);
    if (!device) continue;
    device.utilizationPercent = parseCsvNumber(chip[2]);
    // Prefer HBM pair (last) when present
    device.memoryUsedMb = parseCsvNumber(chip[5]);
    device.memoryTotalMb = parseCsvNumber(chip[6]);
  }
  return devices;
}

function parseAscendTypedSections(sectionText) {
  const devices = [];
  const chunks = String(sectionText || "").split(/__NC_NPU_DEVICE__=/);
  for (const chunk of chunks) {
    const trimmed = chunk.trim();
    if (!trimmed || trimmed.startsWith("__NC_")) continue;
    const nl = trimmed.indexOf("\n");
    const idPart = nl === -1 ? trimmed : trimmed.slice(0, nl);
    const body = nl === -1 ? "" : trimmed.slice(nl + 1);
    const index = Number.parseInt(idPart, 10);
    if (!Number.isFinite(index)) continue;
    devices.push(parseAscendDeviceBlock(index, body));
  }
  return devices;
}

function parseAscendProcesses(sectionText) {
  const processes = [];
  for (const line of String(sectionText || "").split("\n")) {
    // Common proc-mem lines include NPU/Chip/Pid/Name/Memory
    const match = line.match(
      /(?:NPU|Device)?\s*I?D?\s*[:=]?\s*(\d+).*?\b(?:PID|Pid)\s*[:=]?\s*(\d+).*?\b(?:Name|Process)\s*[:=]?\s*(\S+).*?(?:Memory|Mem)\s*[:=]?\s*(\d+(?:\.\d+)?)/i,
    );
    if (match) {
      processes.push({
        vendor: "ascend",
        gpuIndex: Number.parseInt(match[1], 10) || 0,
        pid: Number.parseInt(match[2], 10),
        processName: match[3],
        memoryUsedMb: parseCsvNumber(match[4]),
      });
      continue;
    }

    // Pipe-separated: | 0 | 0 | 12345 | python | 1024 |
    const pipeTable = line.match(
      /^\|\s*(\d+)\s*\|\s*\d+\s*\|\s*(\d+)\s*\|\s*([^|]+?)\s*\|\s*(\d+(?:\.\d+)?)/,
    );
    if (pipeTable) {
      processes.push({
        vendor: "ascend",
        gpuIndex: Number.parseInt(pipeTable[1], 10) || 0,
        pid: Number.parseInt(pipeTable[2], 10),
        processName: pipeTable[3].trim(),
        memoryUsedMb: parseCsvNumber(pipeTable[4]),
      });
      continue;
    }

    // Whitespace-delimited inside one outer |: | 0 0 12345 python 1024 |
    const wsTable = line.match(
      /^\|\s*(\d+)\s+(\d+)\s+(\d+)\s+(\S+)\s+(\d+(?:\.\d+)?)\s*\|?\s*$/,
    );
    if (!wsTable) continue;
    processes.push({
      vendor: "ascend",
      gpuIndex: Number.parseInt(wsTable[1], 10) || 0,
      pid: Number.parseInt(wsTable[3], 10),
      processName: wsTable[4],
      memoryUsedMb: parseCsvNumber(wsTable[5]),
    });
  }
  return processes.filter((p) => Number.isFinite(p.pid) && p.pid > 0);
}

function sliceMarkedSection(text, beginMarker, endMarkers) {
  const start = text.indexOf(beginMarker);
  if (start === -1) return "";
  const from = start + beginMarker.length;
  let end = text.length;
  for (const marker of endMarkers) {
    const idx = text.indexOf(marker, from);
    if (idx !== -1 && idx < end) end = idx;
  }
  return text.slice(from, end);
}

function parseAcceleratorSnapshot(stdout) {
  const text = String(stdout || "");
  const nvidiaDevicesText = sliceMarkedSection(text, "__NC_NVIDIA_DEVICES__", [
    "__NC_NVIDIA_PROCESSES__",
    "__NC_NPU_BEGIN__",
    "__NC_ACCEL_END__",
  ]);
  const nvidiaProcessesText = sliceMarkedSection(text, "__NC_NVIDIA_PROCESSES__", [
    "__NC_NPU_BEGIN__",
    "__NC_ACCEL_END__",
  ]);
  const npuSection = sliceMarkedSection(text, "__NC_NPU_BEGIN__", ["__NC_NPU_END__", "__NC_ACCEL_END__"]);

  const nvidiaDevices = parseNvidiaDevices(nvidiaDevicesText);
  const nvidiaProcesses = parseNvidiaProcesses(nvidiaProcessesText, nvidiaDevices);

  let ascendDevices = parseAscendTypedSections(npuSection);
  if (ascendDevices.length === 0) {
    const infoDump = sliceMarkedSection(npuSection, "__NC_NPU_INFO__", [
      "__NC_NPU_PROCS__",
      "__NC_NPU_END__",
    ]);
    ascendDevices = parseAscendInfoTable(infoDump || npuSection);
  }
  const ascendProcText = sliceMarkedSection(npuSection, "__NC_NPU_PROCS__", [
    "__NC_NPU_END__",
  ]);
  const ascendProcesses = parseAscendProcesses(ascendProcText);

  const devices = [...nvidiaDevices, ...ascendDevices].sort((a, b) => {
    if (a.vendor !== b.vendor) return a.vendor.localeCompare(b.vendor);
    return a.index - b.index;
  });
  const processes = [...nvidiaProcesses, ...ascendProcesses];
  const nvidiaDriverVersion = nvidiaDevices.find((d) => d.driverVersion)?.driverVersion || null;

  return {
    devices,
    processes,
    nvidiaDriverVersion,
    probedAt: Date.now(),
  };
}

function createGpuOpsApi({
  execOnSession,
  execOnLocalMachine,
  isLocalSession,
  process: nodeProcess = process,
}) {
  async function listAccelerators(event, sessionId) {
    if (!sessionId) return { success: false, error: "Missing sessionId" };

    let result;
    if (
      typeof isLocalSession === "function"
      && isLocalSession(sessionId)
      && nodeProcess.platform === "win32"
      && typeof execOnLocalMachine === "function"
    ) {
      result = await execOnLocalMachine(ACCELERATOR_COLLECT_SCRIPT_WINDOWS, 15000);
    } else {
      result = await execOnSession(event, sessionId, ACCELERATOR_COLLECT_SCRIPT, 15000);
    }

    if (result.pending) return { success: false, pending: true };
    if (!result.success) return { success: false, error: result.error || "Failed to query accelerators" };
    const snapshot = parseAcceleratorSnapshot(result.stdout);
    return { success: true, ...snapshot };
  }

  return { listAccelerators, parseAcceleratorSnapshot };
}

module.exports = {
  createGpuOpsApi,
  parseAcceleratorSnapshot,
  parseNvidiaDevices,
  parseNvidiaProcesses,
  parseAscendDeviceBlock,
  parseAscendInfoTable,
  parseAscendProcesses,
  ACCELERATOR_COLLECT_SCRIPT,
  ACCELERATOR_COLLECT_SCRIPT_WINDOWS,
};
