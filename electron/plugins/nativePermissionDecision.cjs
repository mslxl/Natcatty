"use strict";

const MAX_VISIBLE_RESOURCES = 20;

function describePermissionRequest(request) {
  const identity = request.pluginName
    ? `${request.pluginName} (${request.pluginId})`
    : request.pluginId;
  const resources = request.resources ?? [];
  const resourceKinds = request.resourceKinds ?? [];
  const visibleResources = resources.slice(0, MAX_VISIBLE_RESOURCES).map((resource, index) => {
    const suffix = resourceKinds[index] === "directory" ? " (directory and descendants)" : "";
    return `- ${resource}${suffix}`;
  });
  if (resources.length > visibleResources.length) {
    visibleResources.push(`- …and ${resources.length - visibleResources.length} more`);
  }
  return [
    `Plugin: ${identity}`,
    ...(request.publisher ? [`Publisher: ${request.publisher}`] : []),
    `Permission: ${request.permission}`,
    `Reason: ${request.reason}`,
    ...(visibleResources.length ? ["Resources:", ...visibleResources] : []),
  ].join("\n");
}

function createNativePermissionDecisionProvider(options) {
  if (!options?.dialog || typeof options.dialog.showMessageBox !== "function") {
    throw new TypeError("Native plugin permission decisions require an Electron dialog");
  }
  const dialog = options.dialog;
  const window = options.window;
  return async (request, context = {}) => {
    context.signal?.throwIfAborted();
    const choices = [
      { label: "Deny", decision: "deny" },
      { label: "Allow Once", decision: "allow", scope: "once" },
      ...(request.sessionId
        ? [{ label: "Allow for Session", decision: "allow", scope: "session" }]
        : []),
      { label: "Allow for Application", decision: "allow", scope: "application" },
      { label: "Always Allow", decision: "allow", scope: "always" },
    ];
    const messageBoxOptions = {
      type: "warning",
      title: "Plugin permission request",
      message: `${request.pluginName ?? request.pluginId} requests ${request.permission}`,
      detail: describePermissionRequest(request),
      buttons: choices.map(({ label }) => label),
      defaultId: 0,
      cancelId: 0,
      noLink: true,
      ...(context.signal ? { signal: context.signal } : {}),
    };
    const result = window
      ? await dialog.showMessageBox(window, messageBoxOptions)
      : await dialog.showMessageBox(messageBoxOptions);
    context.signal?.throwIfAborted();
    const choice = choices[result.response] ?? choices[0];
    if (choice.decision !== "allow") {
      return Object.freeze({ requestId: request.requestId, decision: "deny" });
    }
    return Object.freeze({
      requestId: request.requestId,
      decision: "allow",
      scope: choice.scope,
    });
  };
}

module.exports = {
  MAX_VISIBLE_RESOURCES,
  createNativePermissionDecisionProvider,
  describePermissionRequest,
};
