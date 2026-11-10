"use strict";

const result = document.getElementById("result");

document.getElementById("origin").textContent =
  "Origin: " + window.location.origin;
document.getElementById("locale").textContent =
  "Locale " + document.documentElement.lang + " (" +
  document.documentElement.dir + ")";

document.getElementById("greet").addEventListener("click", async () => {
  result.textContent = "Waiting for the plugin backend…";
  try {
    const response = await fetch("/__bridge", {
      method: "POST",
      headers: { "X-Natcatty-Method": "greet" },
      body: "embedded page",
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
});
