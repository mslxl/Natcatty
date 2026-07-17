#!/usr/bin/env node

import process from "node:process";

import { buildPlugin, initPlugin, packPlugin, validateTarget } from "./commands.js";

const USAGE = `Netcatty plugin CLI (API 0.1.0-internal)

Usage:
  netcatty-plugin init <directory> --id <reverse.dns.id> [--name <display name>]
  netcatty-plugin validate <directory|package.ncpkg>
  netcatty-plugin build <directory>
  netcatty-plugin pack <directory> [--out <package.ncpkg>]
`;

function optionValue(args: readonly string[], name: string): string | undefined {
  const index = args.indexOf(name);
  if (index === -1) return undefined;
  const value = args[index + 1];
  if (!value || value.startsWith("--")) throw new Error(`Missing value for ${name}`);
  return value;
}

async function main(args: readonly string[]): Promise<void> {
  const [command, target] = args;
  if (!command || command === "help" || command === "--help" || command === "-h") {
    process.stdout.write(USAGE);
    return;
  }
  if (!target) throw new Error(`Missing target for ${command}`);

  if (command === "init") {
    const id = optionValue(args, "--id");
    if (!id) throw new Error("init requires --id <reverse.dns.id>");
    const directory = await initPlugin(target, { id, name: optionValue(args, "--name") });
    process.stdout.write(`Initialized plugin in ${directory}\n`);
    return;
  }
  if (command === "validate") {
    const result = await validateTarget(target);
    process.stdout.write(
      `Valid ${result.kind}: ${result.manifest.id}@${result.manifest.version}\n`,
    );
    return;
  }
  if (command === "build") {
    await buildPlugin(target);
    process.stdout.write("Plugin build completed.\n");
    return;
  }
  if (command === "pack") {
    const result = await packPlugin(target, optionValue(args, "--out"));
    process.stdout.write(
      `Packed ${result.fileCount} files to ${result.outputPath}\nSHA-256 ${result.sha256}\n`,
    );
    return;
  }
  throw new Error(`Unknown command: ${command}`);
}

main(process.argv.slice(2)).catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
  process.exitCode = 1;
});
