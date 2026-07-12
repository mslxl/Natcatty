import type { VaultImportFormat } from "../../domain/vaultImport";
import { readTextFile } from "../../lib/readTextFile";

export const readVaultImportFile = (
  format: VaultImportFormat,
  file: File,
): Promise<string> =>
  readTextFile(file, {
    fallbackEncoding: format === "mobaxterm" ? "gb18030" : undefined,
  });
