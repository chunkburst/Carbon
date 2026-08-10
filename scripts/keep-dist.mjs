#!/usr/bin/env node
// vite empties web/dist on each build, which deletes the committed dist/.gitkeep that
// keeps `go build` (//go:embed all:dist) compiling before the UI is built. Recreate it
// after every build so the working tree never shows the placeholder as deleted.
import { writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
writeFileSync(path.join(root, "web", "dist", ".gitkeep"), "");
