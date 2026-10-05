/**
 * Smoke test for the dashboard bundle.
 *
 * Nothing else in this repo executes the UI, so without this a typo in a
 * component would only surface as a blank page on the VPS. It mounts the real
 * production bundle in jsdom against a live API server and asserts the page
 * actually renders the data it fetched.
 *
 * Usage:
 *   go run ./cmd/server                 # in another shell
 *   node --experimental-vm-modules test/smoke.mjs
 *
 * The server URL can be overridden with DEPLOY_PLATFORM_URL.
 */
import { JSDOM, VirtualConsole } from "jsdom";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const bundlePath = resolve(here, "../../internal/app/static/app.js");
const htmlPath = resolve(here, "../../internal/app/static/index.html");

const baseURL = process.env.DEPLOY_PLATFORM_URL ?? "http://127.0.0.1:8080";

const failures = [];
function check(name, condition, detail = "") {
  if (condition) {
    console.log(`  PASS  ${name}`);
  } else {
    console.log(`  FAIL  ${name}${detail ? ` -- ${detail}` : ""}`);
    failures.push(name);
  }
}

const bundle = await readFile(bundlePath, "utf8");
const html = await readFile(htmlPath, "utf8");

const virtualConsole = new VirtualConsole();
const consoleErrors = [];
virtualConsole.on("jsdomError", (error) => consoleErrors.push(String(error)));
virtualConsole.on("error", (...args) => consoleErrors.push(args.join(" ")));

const dom = new JSDOM(html, {
  url: `${baseURL}/`,
  runScripts: "dangerously",
  pretendToBeVisual: true,
  virtualConsole,
});

// jsdom implements neither fetch nor EventSource, and both are load-bearing
// here: without fetch the dashboard cannot load any data, and without
// EventSource it cannot stream logs. fetch is delegated to Node's implementation
// with relative URLs resolved against the server, so the test exercises the real
// API rather than a stub.
dom.window.fetch = (input, init) => {
  const url = typeof input === "string" ? new URL(input, baseURL).href : input;
  return fetch(url, init);
};

const eventSources = [];
dom.window.EventSource = class {
  constructor(url) {
    this.url = url;
    this.listeners = new Map();
    eventSources.push(this);
  }
  addEventListener(type, handler) {
    this.listeners.set(type, handler);
  }
  removeEventListener(type) {
    this.listeners.delete(type);
  }
  close() {
    this.closed = true;
  }
};

const root = dom.window.document.querySelector("#root");

// The bundle is an ES module; evaluate it as one via a blob-free shim so the
// browser's module loader is not required.
dom.window.eval(`(async () => { ${bundle} })()`);

const sleep = (ms) => new Promise((done) => setTimeout(done, ms));

// Wait for the initial fetches to settle, then poll until the project list
// has rendered or the deadline passes.
let waited = 0;
while (waited < 5000 && !root.querySelector(".card, .empty__title")) {
  await sleep(100);
  waited += 100;
}
await sleep(400);

console.log("\ndashboard smoke test");

check("bundle mounts into #root", root.children.length > 0, `root has ${root.children.length} children`);
check(
  "renders the topbar heading",
  root.querySelector("h1")?.textContent?.includes("Deploy Platform") ?? false,
);
check("renders the project form", root.querySelector("#root form input") !== null);

// The API must be reachable and the dashboard must have drawn projects or the
// empty state; either proves the fetch -> render path works.
const cards = root.querySelectorAll(".card");
const empty = root.querySelector(".empty__title");
check(
  "project list rendered from the API",
  cards.length > 0 || empty !== null,
  `cards=${cards.length} empty=${empty?.textContent ?? "none"}`,
);

check("renders deployment rows or an empty state", root.querySelector(".row") !== null || empty !== null);
check("renders the environment panel", root.textContent.includes("Environment"));

const fatalConsoleErrors = consoleErrors.filter(
  (message) => !message.includes("Not implemented") && !message.includes("EventSource"),
);
check("no uncaught script errors", fatalConsoleErrors.length === 0, fatalConsoleErrors.join(" | "));

if (cards.length > 0) {
  console.log(`\n  info: ${cards.length} project card(s) rendered`);
  const text = root.textContent ?? "";
  check("project card shows a live URL", text.includes("https://"), "no https URL found");
  check("status badge rendered", root.querySelector(".badge") !== null);
}

dom.window.close();

console.log("");
if (failures.length > 0) {
  console.error(`FAILED: ${failures.length} check(s) did not pass`);
  process.exit(1);
}
console.log("all checks passed");
