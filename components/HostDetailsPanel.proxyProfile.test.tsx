import test from "node:test";
import assert from "node:assert/strict";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

import { I18nProvider } from "../application/i18n/I18nProvider.tsx";
import type { Host } from "../types.ts";
import HostDetailsPanel from "./HostDetailsPanel.tsx";

const hostWithMissingProxyProfile: Host = {
  id: "host-1",
  label: "DB",
  hostname: "db.example.com",
  username: "root",
  tags: [],
  os: "linux",
  port: 22,
  protocol: "ssh",
  authMethod: "password",
  proxyProfileId: "missing-proxy",
  createdAt: 1,
};

const renderHostDetails = () =>
  renderToStaticMarkup(
    React.createElement(
      I18nProvider,
      { locale: "en" },
      React.createElement(HostDetailsPanel, {
        initialData: hostWithMissingProxyProfile,
        availableKeys: [],
        identities: [],
        proxyProfiles: [],
        groups: [],
        managedSources: [],
        allTags: [],
        allHosts: [],
        terminalThemeId: "default",
        terminalFontSize: 14,
        onSave: () => {},
        onCancel: () => {},
      }),
    ),
  );

test("HostDetailsPanel shows a missing saved proxy without undefined fields", () => {
  const markup = renderHostDetails();

  assert.match(markup, /Missing saved proxy/);
  assert.doesNotMatch(markup, /undefined:undefined/);
});
