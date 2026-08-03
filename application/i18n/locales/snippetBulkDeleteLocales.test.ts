import assert from 'node:assert/strict';
import test from 'node:test';

import en from './en.ts';
import ru from './ru.ts';
import zhCN from './zh-CN.ts';
import zhTW from './zh-TW.ts';

const KEYS = [
  'snippets.selection.deleteSelected',
  'snippets.selection.deleteConfirmTitle',
  'snippets.selection.deleteConfirmDesc',
  'snippets.selection.deleteSuccess',
] as const;

test('snippet bulk-delete copy exists in every locale', () => {
  for (const [locale, messages] of Object.entries({ en, ru, zhCN, zhTW })) {
    const missing = KEYS.filter((key) => !messages[key]);
    assert.deepEqual(missing, [], `${locale} is missing snippet bulk-delete copy`);
  }
});
