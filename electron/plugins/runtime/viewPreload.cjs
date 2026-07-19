"use strict";

const { contextBridge, ipcRenderer } = require("electron");

function subscribe(channel, listener) {
  if (typeof listener !== "function") throw new TypeError("Plugin view listener must be a function");
  const handler = (_event, value) => listener(value);
  ipcRenderer.on(channel, handler);
  return Object.freeze({ dispose: () => ipcRenderer.removeListener(channel, handler) });
}

contextBridge.exposeInMainWorld("netcattyView", Object.freeze({
  postMessage(message) {
    return ipcRenderer.invoke("netcatty-plugin-view:post-message", message);
  },
  executeCommand(command, args) {
    return ipcRenderer.invoke("netcatty-plugin-view:execute-command", { command, args });
  },
  getState() {
    return ipcRenderer.invoke("netcatty-plugin-view:get-state");
  },
  setState(state) {
    return ipcRenderer.invoke("netcatty-plugin-view:set-state", state);
  },
  onDidReceiveMessage(listener) {
    return subscribe("netcatty-plugin-view:message", listener);
  },
  onDidChangeEnvironment(listener) {
    return subscribe("netcatty-plugin-view:environment", listener);
  },
}));
