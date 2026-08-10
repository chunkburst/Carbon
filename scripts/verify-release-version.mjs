import { execFileSync } from "node:child_process";
import { readFileSync, realpathSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const stableTag = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = realpathSync(path.join(scriptDirectory, ".."));

function fail(message) {
  throw new Error(message);
}

function readUtf8(relativePath) {
  return readFileSync(path.join(repositoryRoot, relativePath), "utf8");
}

function readJsonVersion(relativePath) {
  const parsed = JSON.parse(readUtf8(relativePath));
  if (typeof parsed.version !== "string") {
    fail(`${relativePath} must contain a string version.`);
  }
  return parsed.version;
}

function readTomlPackageVersion(relativePath, packageName) {
  const lines = readUtf8(relativePath).split(/\r?\n/);
  let inPackage = false;
  let foundName = null;
  let foundVersion = null;
  for (const line of lines) {
    const section = line.match(/^\s*\[([^\]]+)\]\s*(?:#.*)?$/);
    if (section) {
      if (inPackage) break;
      inPackage = section[1] === "package";
      continue;
    }
    if (!inPackage) continue;
    const name = line.match(/^\s*name\s*=\s*"([^"]+)"\s*(?:#.*)?$/);
    if (name) foundName = name[1];
    const version = line.match(/^\s*version\s*=\s*"([^"]+)"\s*(?:#.*)?$/);
    if (version) foundVersion = version[1];
  }
  if (foundName !== packageName || !foundVersion) {
    fail(`${relativePath} must contain [package] name = "${packageName}" and a version.`);
  }
  return foundVersion;
}

function readCargoLockPackageVersion(relativePath, packageName) {
  const packageBlocks = readUtf8(relativePath).split(/^\[\[package\]\]\s*$/m).slice(1);
  const matches = packageBlocks
    .map((block) => ({
      name: block.match(/^name\s*=\s*"([^"]+)"\s*$/m)?.[1],
      version: block.match(/^version\s*=\s*"([^"]+)"\s*$/m)?.[1],
    }))
    .filter((pkg) => pkg.name === packageName);
  if (matches.length !== 1 || !matches[0].version) {
    fail(`${relativePath} must contain exactly one ${packageName} package with a version.`);
  }
  return matches[0].version;
}

function assertNoTrackedReleaseData() {
  const output = execFileSync("git", ["-C", repositoryRoot, "ls-files", "-z"], {
    encoding: "buffer",
    stdio: ["ignore", "pipe", "pipe"],
  });
  const forbiddenDirectoryPatterns = [
    /(^|\/)\.carbon(?:\/|$)/i,
    /(^|\/)\.cairn(?:\/|$)/i,
    /(^|\/)\.codex(?:\/|$)/i,
    /(^|\/)\.codex-tmp(?:\/|$)/i,
    /(^|\/)\.codex-test-tmp(?:\/|$)/i,
    /(^|\/)docs\/reports(?:\/|$)/i,
    /(^|\/)node_modules(?:\/|$)/i,
    /(^|\/)target(?:\/|$)/i,
    /(^|\/)release-assets(?:\/|$)/i,
    /(^|\/)archives(?:\/|$)/i,
    /(^|\/)backups(?:\/|$)/i,
  ];
  const forbiddenFilePatterns = [
    /(^|\/)\.cairn-cluster\.json$/i,
    /(^|\/)(?:\.env|[^/]+\.env)(?:\.[^/]+)?$/i,
    /\.(?:key|pem|p12|pfx|crt|cer|cert|der)$/i,
    /\.(?:db|sqlite|sqlite3)$/i,
    /\.(?:zip|7z|rar|tar|gz|bz2|xz)$/i,
    /\.log$/i,
  ];
  const trackedPaths = output
    .toString("utf8")
    .split("\0")
    .filter(Boolean)
    .map((entry) => entry.replaceAll("\\", "/"));
  const blocked = trackedPaths.filter((entry) =>
    forbiddenDirectoryPatterns.some((pattern) => pattern.test(entry)) ||
    forbiddenFilePatterns.some((pattern) => pattern.test(entry)),
  );
  if (blocked.length > 0) {
    fail(`Release source tree contains prohibited tracked paths:\n${blocked.map((entry) => `  - ${entry}`).join("\n")}`);
  }
}

const [tag, ...extraArguments] = process.argv.slice(2);
if (!tag || extraArguments.length > 0) {
  fail("usage: node scripts/verify-release-version.mjs vX.Y.Z");
}
const tagMatch = stableTag.exec(tag);
if (!tagMatch) {
  fail(`Release tag must be a stable SemVer tag in vX.Y.Z form: ${tag}`);
}
const expectedVersion = tag.slice(1);
const versions = new Map([
  ["desktop/src-tauri/tauri.conf.json", readJsonVersion("desktop/src-tauri/tauri.conf.json")],
  ["desktop/src-tauri/Cargo.toml", readTomlPackageVersion("desktop/src-tauri/Cargo.toml", "carbon-desktop")],
  ["desktop/src-tauri/Cargo.lock", readCargoLockPackageVersion("desktop/src-tauri/Cargo.lock", "carbon-desktop")],
  ["desktop/package.json", readJsonVersion("desktop/package.json")],
  ["web/package.json", readJsonVersion("web/package.json")],
  ["docs/package.json", readJsonVersion("docs/package.json")],
]);

const mismatches = [...versions.entries()].filter(([, version]) => version !== expectedVersion);
if (mismatches.length > 0) {
  fail(
    `Release tag ${tag} requires version ${expectedVersion} in every release manifest; found:\n${mismatches
      .map(([file, version]) => `  - ${file}: ${version}`)
      .join("\n")}`,
  );
}

assertNoTrackedReleaseData();
console.log(`Verified committed release version ${expectedVersion} across ${versions.size} manifests and the tracked-path gate.`);
