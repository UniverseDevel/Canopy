// Builds the UI into internal/web/dist, which the Go binary embeds.
import * as esbuild from "esbuild";
import { copyFileSync, mkdirSync } from "node:fs";

const out = "../internal/web/dist";
mkdirSync(`${out}/assets`, { recursive: true });
copyFileSync("index.html", `${out}/index.html`);

const opts = {
  entryPoints: { app: "src/main.ts", style: "src/style.css" },
  bundle: true,
  minify: true,
  sourcemap: false,
  target: ["chrome110", "firefox110", "safari16", "edge110"],
  outdir: `${out}/assets`,
  logLevel: "info",
};

if (process.argv.includes("--watch")) {
  const ctx = await esbuild.context(opts);
  await ctx.watch();
} else {
  await esbuild.build(opts);
}
