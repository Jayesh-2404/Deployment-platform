/**
 * Decides whether the EnvPanel crash is our component or a Preact-in-jsdom
 * artifact, by mounting the same hook shape with real Preact.
 */
import { JSDOM } from "jsdom";

const bundleUrl = new URL("./hooks-isolation.bundle.js", import.meta.url);
const bundle = await import("node:fs/promises").then((fs) => fs.readFile(bundleUrl, "utf8"));

const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', {
  url: "http://localhost/",
  runScripts: "dangerously",
  pretendToBeVisual: true,
});
dom.window.eval(bundle);
dom.window.__mount(dom.window.document.querySelector("#root"));
await new Promise((r) => setTimeout(r, 500));

const results = dom.window.__results;
console.log("simple    :", JSON.stringify(results.simple));
console.log("withEffect:", JSON.stringify(results.withEffect));
console.log("child     :", JSON.stringify(results.child));
console.log("childShape:", JSON.stringify(results.childShape));

const bad = Object.entries(results).filter(([, value]) => value === null || value === undefined);
if (bad.length > 0) {
  console.log("\nRESULT: Preact hooks produced null/undefined inside jsdom.");
  console.log("This is an environment artifact, not a bug in the dashboard.");
  process.exit(0);
}
console.log("\nRESULT: Preact hooks work under jsdom, so the EnvPanel crash is ours.");
