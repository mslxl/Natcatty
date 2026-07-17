export const PLUGIN_API_VERSION = "0.1.0-internal" as const;
export const PLUGIN_MANIFEST_FILE = "netcatty.plugin.json" as const;
export const PLUGIN_PACKAGE_EXTENSION = ".ncpkg" as const;

export type * from "./generated/plugin-contract.js";
