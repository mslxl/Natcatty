import assert from 'node:assert/strict';
import test from 'node:test';

import {
  appendTerminalPromptSecurityTail,
  isConfirmedTerminalShellPrompt,
  isSensitiveTerminalChallenge,
  isUntrustedTerminalInputPrompt,
  shouldUsePluginTerminalCompletionProvider,
} from './terminalPromptSecurity.ts';

test('terminal prompt security recognizes password, MFA, OTP, PIN, and CJK challenges', () => {
  const challenges = [
    'Password: ',
    'Enter passphrase> ',
    'OTP> 123456',
    'One-time password: 123456',
    'Verification code> 123456',
    'Duo passcode: 123456',
    'MFA token: 123456',
    'Authentication code: 123456',
    'Security PIN: 123456',
    '验证码> 123456',
    '动态口令：123456',
    '二次认证密码：123456',
  ];
  for (const challenge of challenges) {
    assert.equal(isSensitiveTerminalChallenge(challenge), true, challenge);
  }
  assert.equal(isSensitiveTerminalChallenge('password authentication succeeded'), false);
  assert.equal(isSensitiveTerminalChallenge('echo OTP'), false);
});

test('terminal prompt security combines split output chunks within a bounded logical line', () => {
  let tail = appendTerminalPromptSecurityTail('', '\u001b[33mVerifi');
  tail = appendTerminalPromptSecurityTail(tail, 'cation code\u001b[0m> ');
  assert.equal(isSensitiveTerminalChallenge(tail), true);
  tail = appendTerminalPromptSecurityTail(tail, '\r\nuser@host:~$ ');
  assert.equal(isSensitiveTerminalChallenge(tail), false);
  assert.ok(tail.length <= 2_048);
});

test('terminal prompt security positively confirms shell prompts and fails closed on ambiguous prompts', () => {
  const confirmed = [
    '$ ',
    'root# ',
    'user@host:~/repo$ ',
    '➜  ~/repo ',
    '\uE0B0 ',
    'PS C:\\Users\\alice> ',
    'C:\\Users\\alice> ',
    'user@host> ',
  ];
  for (const prompt of confirmed) {
    assert.equal(isConfirmedTerminalShellPrompt(prompt), true, prompt);
  }
  assert.equal(isConfirmedTerminalShellPrompt('OTP> '), false);
  assert.equal(isConfirmedTerminalShellPrompt('custom> '), false);
  assert.equal(isConfirmedTerminalShellPrompt('router> ', { allowHostStyleGreaterThan: true }), true);
});

test('plugin terminal completion policy requires a non-sensitive confirmed shell prompt', () => {
  assert.equal(shouldUsePluginTerminalCompletionProvider({
    sensitiveInputActive: false,
    promptText: 'user@host:~$ ',
  }), true);
  assert.equal(shouldUsePluginTerminalCompletionProvider({
    sensitiveInputActive: true,
    promptText: 'user@host:~$ ',
  }), false);
  assert.equal(shouldUsePluginTerminalCompletionProvider({
    sensitiveInputActive: false,
    promptText: 'OTP> ',
  }), false);
  assert.equal(shouldUsePluginTerminalCompletionProvider({
    sensitiveInputActive: false,
    promptText: 'custom> ',
  }), false);
});

test('unknown prompt-shaped authentication boundaries fail closed', () => {
  assert.equal(isUntrustedTerminalInputPrompt('Custom authentication> '), true);
  assert.equal(isUntrustedTerminalInputPrompt('Please authenticate: '), true);
  assert.equal(isUntrustedTerminalInputPrompt('alice@host:~$ '), false);
  assert.equal(isUntrustedTerminalInputPrompt('router> ', { allowHostStyleGreaterThan: true }), false);
  assert.equal(isUntrustedTerminalInputPrompt('router> '), true);
});
