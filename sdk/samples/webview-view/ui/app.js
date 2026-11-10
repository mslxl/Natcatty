"use strict";

const origin = document.getElementById("origin");
const locale = document.getElementById("locale");
const result = document.getElementById("result");

function showEnvironment() {
  origin.textContent =
    "Origin: " + window.location.origin + " — bridge methods: greet, report";
  locale.textContent =
    "Locale " + document.documentElement.lang + " (" +
    document.documentElement.dir + ")";
}
showEnvironment();

// The sandbox bridge is a same-origin POST to a reserved path. The host
// identifies the calling page itself, so this page can only ask for the
// methods its manifest declares.
async function call(method, payload) {
  result.textContent = "Waiting for the plugin backend…";
  try {
    const response = await fetch("/__bridge", {
      method: "POST",
      headers: { "X-Natcatty-Method": method },
      body: payload,
      cache: "no-store",
      credentials: "omit",
    });
    const answer = await response.text();
    if (!response.ok) {
      throw new Error(answer || "bridge rejected (" + response.status + ")");
    }
    result.textContent = answer;
  } catch (error) {
    result.textContent = "Bridge rejected: " + String(error);
  }
}

document.getElementById("greet").addEventListener("click", () => {
  call("greet", document.getElementById("name").value);
});
document.getElementById("report").addEventListener("click", () => {
  call("report", "");
});
