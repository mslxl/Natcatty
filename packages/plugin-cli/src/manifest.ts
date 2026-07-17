import { createRequire } from "node:module";
import { readFile } from "node:fs/promises";
import path from "node:path";

import type {
  PermissionDeclaration,
  PluginManifest,
  PluginPermission,
} from "@netcatty/plugin-contract";
import Ajv2020, { type ErrorObject } from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

import { PACKAGE_LIMITS } from "./constants.js";
import { assertSafePackagePath } from "./packagePath.js";

const require = createRequire(import.meta.url);
const schemaPath = require.resolve(
  "@netcatty/plugin-contract/schema/plugin-contract.schema.json",
);
const contractSchema = JSON.parse(await readFile(schemaPath, "utf8")) as object;
const ajv = new Ajv2020({ allErrors: true, strict: true });
addFormats(ajv);
const validateSchema = ajv.compile<PluginManifest>(contractSchema);

export interface ManifestValidationResult {
  readonly valid: boolean;
  readonly manifest?: PluginManifest;
  readonly errors: readonly string[];
}

function formatAjvError(error: ErrorObject): string {
  const location = error.instancePath || "/";
  return `${location} ${error.message ?? "is invalid"}`;
}

function permissionName(declaration: PermissionDeclaration): PluginPermission {
  return typeof declaration === "string" ? declaration : declaration.permission;
}

function findDuplicateIds(manifest: PluginManifest): string[] {
  const errors: string[] = [];
  const groups = [
    ["settings", manifest.contributes?.settings],
    ["commands", manifest.contributes?.commands],
    ["views", manifest.contributes?.views],
    ["providers", manifest.contributes?.providers],
    ["companionExecutables", manifest.companionExecutables],
  ] as const;
  for (const [groupName, contributions] of groups) {
    const ids = new Set<string>();
    for (const contribution of contributions ?? []) {
      if (ids.has(contribution.id)) {
        errors.push(`Duplicate ${groupName} id: ${contribution.id}`);
      }
      ids.add(contribution.id);
    }
  }
  return errors;
}

function validateSemantics(manifest: PluginManifest): string[] {
  const errors = findDuplicateIds(manifest);
  const requiredPermissions = new Set(
    (manifest.permissions?.required ?? []).map(permissionName),
  );
  for (const declaration of manifest.permissions?.optional ?? []) {
    const permission = permissionName(declaration);
    if (requiredPermissions.has(permission)) {
      errors.push(`Permission cannot be both required and optional: ${permission}`);
    }
  }

  const commandIds = new Set((manifest.contributes?.commands ?? []).map(({ id }) => id));
  for (const menu of manifest.contributes?.menus ?? []) {
    if (!commandIds.has(menu.command)) {
      errors.push(`Menu references an undeclared command: ${menu.command}`);
    }
  }

  for (const setting of manifest.contributes?.settings ?? []) {
    if (setting.secret && setting.default !== undefined) {
      errors.push(`Secret setting must not declare a default value: ${setting.id}`);
    }
    if (["select", "multiselect"].includes(setting.control) && !setting.options?.length) {
      errors.push(`${setting.control} setting requires options: ${setting.id}`);
    }
    if (setting.control === "number" && setting.minimum !== undefined
      && setting.maximum !== undefined && setting.minimum > setting.maximum) {
      errors.push(`Number setting minimum exceeds maximum: ${setting.id}`);
    }
  }

  for (const entryPath of [
    manifest.main.browser,
    manifest.main.node,
    ...(manifest.contributes?.views ?? []).map(({ entry }) => entry),
    ...(manifest.companionExecutables ?? []).map(({ path: companionPath }) => companionPath),
  ]) {
    if (entryPath) {
      try {
        assertSafePackagePath(entryPath);
      } catch (error) {
        errors.push(error instanceof Error ? error.message : String(error));
      }
    }
  }
  return errors;
}

export function validateManifestValue(value: unknown): ManifestValidationResult {
  if (!validateSchema(value)) {
    return {
      valid: false,
      errors: (validateSchema.errors ?? []).map(formatAjvError),
    };
  }
  const semanticErrors = validateSemantics(value);
  return semanticErrors.length === 0
    ? { valid: true, manifest: value, errors: [] }
    : { valid: false, errors: semanticErrors };
}

export async function readAndValidateManifest(
  pluginDirectory: string,
): Promise<PluginManifest> {
  const manifestPath = path.join(pluginDirectory, "netcatty.plugin.json");
  const contents = await readFile(manifestPath);
  if (contents.byteLength > PACKAGE_LIMITS.manifestBytes) {
    throw new Error(`Plugin manifest exceeds ${PACKAGE_LIMITS.manifestBytes} bytes`);
  }
  let value: unknown;
  try {
    value = JSON.parse(contents.toString("utf8"));
  } catch (error) {
    throw new Error(
      `Plugin manifest is not valid JSON: ${error instanceof Error ? error.message : String(error)}`,
    );
  }
  const result = validateManifestValue(value);
  if (!result.valid || !result.manifest) {
    throw new Error(`Plugin manifest is invalid:\n- ${result.errors.join("\n- ")}`);
  }
  return result.manifest;
}
