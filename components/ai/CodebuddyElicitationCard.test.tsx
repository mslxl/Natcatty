import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nProvider } from '../../application/i18n/I18nProvider';
import { CodebuddyElicitationCard } from './CodebuddyElicitationCard';

test('CodebuddyElicitationCard renders MCP form fields and response actions', () => {
  const markup = renderToStaticMarkup(
    <I18nProvider locale="en">
      <CodebuddyElicitationCard
        elicitation={{
          elicitationId: 'el-1',
          chatSessionId: 'chat-1',
          request: {
            message: 'Choose deployment settings',
            requestedSchema: {
              type: 'object',
              properties: {
                environment: {
                  type: 'string',
                  title: 'Environment',
                  enum: ['staging', 'production'],
                },
                dryRun: {
                  type: 'boolean',
                  title: 'Dry run',
                },
              },
              required: ['environment'],
            },
          },
        }}
        onRespond={async () => {}}
      />
    </I18nProvider>,
  );

  assert.match(markup, /CodeBuddy needs your input/);
  assert.match(markup, /Choose deployment settings/);
  assert.match(markup, /Environment \*/);
  assert.match(markup, /staging/);
  assert.match(markup, /Dry run/);
  assert.match(markup, /Decline/);
  assert.match(markup, /Continue/);
});
