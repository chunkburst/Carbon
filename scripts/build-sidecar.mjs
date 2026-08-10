#!/usr/bin/env node
// Builds the Carbon Go binary as a Tauri sidecar. Tauri's bundler expects the file
// named with the Rust target triple (e.g. carbon-aarch64-apple-darwin) and strips the
// suffix when bundling. The Go build embeds web/dist, which `pnpm build` produced first
// (see beforeBuildCommand in tauri.conf.json).
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, "..");
const outDir = path.join(root, "desktop", "src-tauri", "binaries");
const tauriConfig = JSON.parse(
  readFileSync(path.join(root, "desktop", "src-tauri", "tauri.conf.json"), "utf8"),
);
const version = String(tauriConfig.version ?? "").trim();
if (!version) {
  throw new Error("desktop/src-tauri/tauri.conf.json must contain a version");
}

// The Go binary embeds web/dist (//go:embed all:dist), so that directory must exist for the
// build to compile. In production builds `pnpm --dir web build` runs first and fills it; in
// `tauri dev` the UI is served live by Vite, so a stub is enough to let the sidecar build.
const distDir = path.join(root, "web", "dist");
if (!existsSync(path.join(distDir, "index.html"))) {
  mkdirSync(distDir, { recursive: true });
  writeFileSync(path.join(distDir, "index.html"), "<!doctype html>\n");
}

function hostTriple() {
  const output = execFileSync("rustc", ["-vV"], { encoding: "utf8" });
  const hostLine = output.split("\n").find((line) => line.startsWith("host: "));
  if (!hostLine) {
    throw new Error("could not determine Rust host triple");
  }
  return hostLine.slice("host: ".length).trim();
}

const triple =
  process.env.TARGET_TRIPLE || process.env.TAURI_ENV_TARGET_TRIPLE || hostTriple();
const extension = triple.includes("windows") ? ".exe" : "";
const outputPath = path.join(outDir, `carbon-${triple}${extension}`);

// Map the Rust target triple to Go's GOOS/GOARCH so the sidecar matches the bundle's
// architecture. Without this the build follows the host arch — e.g. an arm64 macOS runner
// producing the x86_64-apple-darwin bundle would embed an arm64 binary.
function goEnvFromTriple(t) {
  const arch = t.startsWith("aarch64")
    ? "arm64"
    : t.startsWith("x86_64")
      ? "amd64"
      : t.startsWith("i686")
        ? "386"
        : t.startsWith("armv7")
          ? "arm"
          : null;
  const os = t.includes("apple-darwin")
    ? "darwin"
    : t.includes("windows")
      ? "windows"
      : t.includes("linux")
        ? "linux"
        : null;
  if (!arch || !os) {
    throw new Error(`unsupported target triple: ${t}`);
  }
  return { GOOS: os, GOARCH: arch };
}

const { GOOS, GOARCH } = goEnvFromTriple(triple);

mkdirSync(outDir, { recursive: true });
console.log(`building Carbon sidecar -> binaries/${path.basename(outputPath)} (${GOOS}/${GOARCH})`);

const ldflags = [
  "-s",
  "-w",
  `-X=main.version=${version}`,
  `-X=carbon/internal/mcp.ServiceVersion=${version}`,
].join(" ");

execFileSync("go", ["build", "-ldflags", ldflags, "-o", outputPath, "./cmd/carbon"], {
  cwd: root,
  // CGO off → a static, reliably cross-compilable binary (Carbon is pure Go).
  env: { ...process.env, GOOS, GOARCH, CGO_ENABLED: "0" },
  stdio: "inherit",
});
