import { JSDOM, VirtualConsole } from "jsdom";
import { readFile } from "node:fs/promises";
const bundle = await readFile(new URL("../../internal/app/static/" + (process.env.DEPLOY_PLATFORM_BUNDLE ?? "app.js"), import.meta.url), "utf8");
const html = "<!doctype html><html><body><div id=\"root\"></div></body></html>";
const vc = new VirtualConsole();
vc.on("jsdomError", (e) => console.log("JSDOM ERROR:", e.stack ?? e.message));
const dom = new JSDOM(html, { url: "http://127.0.0.1:8080/", runScripts: "dangerously", pretendToBeVisual: true, virtualConsole: vc });
dom.window.fetch = (i, init) => fetch(typeof i === "string" ? new URL(i, "http://127.0.0.1:8080").href : i, init);
dom.window.EventSource = class { constructor(u){this.u=u;} addEventListener(){} removeEventListener(){} close(){} };
dom.window.eval(`(async () => { ${bundle} })()`);
await new Promise((r) => setTimeout(r, 2500));
console.log("root children:", dom.window.document.querySelector("#root").children.length);

