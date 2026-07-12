import assert from "node:assert/strict";
import test from "node:test";

import { importVaultHostsFromText } from "../../domain/vaultImport.ts";
import { readVaultImportFile } from "./vaultImportFile.ts";

const sessionValue = "#109#0%10.0.0.1%22%root";

test("MobaXterm import decodes legacy GB18030 Chinese text", async () => {
  const prefix = new TextEncoder().encode("[Bookmarks]\nSubRep=\nImgNum=42\n");
  const suffix = new TextEncoder().encode(`=${sessionValue}`);
  const encodedLabel = new Uint8Array([
    0xd6, 0xd0, 0xce, 0xc4, 0xb7, 0xfe, 0xce, 0xf1, 0xc6, 0xf7,
  ]);
  const bytes = new Uint8Array(prefix.length + encodedLabel.length + suffix.length);
  bytes.set(prefix);
  bytes.set(encodedLabel, prefix.length);
  bytes.set(suffix, prefix.length + encodedLabel.length);

  const text = await readVaultImportFile(
    "mobaxterm",
    new File([bytes], "MobaXterm.ini", { type: "text/plain" }),
  );
  const result = importVaultHostsFromText("mobaxterm", text);

  assert.equal(result.hosts[0]?.label, "中文服务器");
});

test("MobaXterm import keeps unmarked UTF-8 Chinese text", async () => {
  const text = `[Bookmarks]\nSubRep=\nImgNum=42\n北京上海=${sessionValue}`;
  const decoded = await readVaultImportFile(
    "mobaxterm",
    new File([text], "MobaXterm.ini", { type: "text/plain" }),
  );
  const result = importVaultHostsFromText("mobaxterm", decoded);

  assert.equal(result.hosts[0]?.label, "北京上海");
});

test("MobaXterm import keeps valid UTF-8 labels when GB18030 would produce Chinese", async () => {
  const text = `[Bookmarks]\nSubRep=\nImgNum=42\n¡prod=${sessionValue}`;
  const decoded = await readVaultImportFile(
    "mobaxterm",
    new File([text], "MobaXterm.ini", { type: "text/plain" }),
  );
  const result = importVaultHostsFromText("mobaxterm", decoded);

  assert.equal(result.hosts[0]?.label, "¡prod");
});
