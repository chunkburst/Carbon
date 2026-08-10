#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(cd -- "$script_dir/.." && pwd -P)"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "macOS release artifacts must be built on macOS." >&2
  exit 1
fi

host_arch="$(uname -m)"
target="${1:-}"
release_arch="${2:-}"
if [[ -z "$target" || -z "$release_arch" ]]; then
  case "$host_arch" in
    arm64)
      target="aarch64-apple-darwin"
      release_arch="arm64"
      ;;
    x86_64)
      target="x86_64-apple-darwin"
      release_arch="x64"
      ;;
    *)
      echo "Unsupported macOS host architecture: $host_arch" >&2
      exit 1
      ;;
  esac
fi

case "$target:$release_arch:$host_arch" in
  aarch64-apple-darwin:arm64:arm64) expected_lipo_arch="arm64" ;;
  x86_64-apple-darwin:x64:x86_64) expected_lipo_arch="x86_64" ;;
  *)
    echo "The release target must match the native runner: target=$target arch=$release_arch host=$host_arch" >&2
    exit 1
    ;;
esac

version="$(node -e 'const c=require(process.argv[1]); const v=String(c.version||"").trim(); if(!/^\d+\.\d+\.\d+$/.test(v)) process.exit(1); process.stdout.write(v)' "$repo_root/desktop/src-tauri/tauri.conf.json")"
if [[ -n "${RELEASE_TAG:-}" && "${RELEASE_TAG#v}" != "$version" ]]; then
  echo "Release tag $RELEASE_TAG does not match desktop version $version." >&2
  exit 1
fi

asset_dir="$repo_root/desktop/src-tauri/target/release-assets/Carbon-$version-macos-$release_arch"
bundle_root="$repo_root/desktop/src-tauri/target/$target/release/bundle"
rm -rf -- "$asset_dir"
mkdir -p -- "$asset_dir"

export TARGET_TRIPLE="$target"
export MACOSX_DEPLOYMENT_TARGET="12.0"
pnpm --dir "$repo_root/desktop" exec tauri build --bundles app,dmg --target "$target" --no-sign

app_dir="$bundle_root/macos/Carbon.app"
if [[ ! -d "$app_dir" ]]; then
  echo "Expected app bundle was not produced: $app_dir" >&2
  exit 1
fi

shopt -s nullglob
dmg_files=("$bundle_root/dmg/"*.dmg)
if [[ "${#dmg_files[@]}" -ne 1 ]]; then
  echo "Expected exactly one DMG, found ${#dmg_files[@]}." >&2
  exit 1
fi

plist="$app_dir/Contents/Info.plist"
main_name="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$plist")"
main_binary="$app_dir/Contents/MacOS/$main_name"
sidecar="$app_dir/Contents/MacOS/carbon"
for binary in "$main_binary" "$sidecar" "$repo_root/desktop/src-tauri/binaries/carbon-$target"; do
  if [[ ! -f "$binary" || ! -x "$binary" ]]; then
    echo "Expected executable is missing from the macOS bundle: $binary" >&2
    exit 1
  fi
  if ! lipo -verify_arch "$expected_lipo_arch" "$binary"; then
    echo "Unexpected architecture for $binary" >&2
    lipo -archs "$binary" >&2 || true
    exit 1
  fi
  minimum_os="$(otool -l "$binary" | awk '
    $1 == "minos" { print $2; exit }
    $1 == "cmd" && $2 == "LC_VERSION_MIN_MACOSX" { legacy = 1; next }
    legacy && $1 == "version" { print $2; exit }
  ')"
  if [[ -z "$minimum_os" ]] || ! awk -v version="$minimum_os" 'BEGIN {
    split(version, parts, ".")
    exit !((parts[1] + 0) < 12 || ((parts[1] + 0) == 12 && (parts[2] + 0) <= 0))
  }'; then
    echo "Executable requires macOS $minimum_os instead of the documented macOS 12 baseline: $binary" >&2
    exit 1
  fi
done
cli_version="$("$repo_root/desktop/src-tauri/binaries/carbon-$target" version)"
if [[ "$cli_version" != "Carbon $version" ]]; then
  echo "Unexpected Carbon CLI version: $cli_version" >&2
  exit 1
fi

bundle_version="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$plist")"
if [[ "$bundle_version" != "$version" ]]; then
  echo "App bundle version $bundle_version does not match $version." >&2
  exit 1
fi
hdiutil verify "${dmg_files[0]}"

dmg_asset="$asset_dir/Carbon-$version-macos-$release_arch.dmg"
cp -p -- "${dmg_files[0]}" "$dmg_asset"

cli_stage="$(mktemp -d "${TMPDIR:-/tmp}/carbon-macos-cli.XXXXXX")"
cleanup() {
  rm -rf -- "$cli_stage"
}
trap cleanup EXIT
cp -p -- "$repo_root/desktop/src-tauri/binaries/carbon-$target" "$cli_stage/carbon"
cp -p -- "$repo_root/LICENSE" "$cli_stage/LICENSE.txt"
printf '%s\n' \
  "Carbon $version CLI for macOS $release_arch" \
  "" \
  "Run ./carbon version, then ./carbon --help." \
  "This archive contains the CLI/Web/MCP sidecar only; use the DMG for the desktop app." \
  > "$cli_stage/README.txt"
chmod 0755 "$cli_stage/carbon"
COPYFILE_DISABLE=1 tar -czf "$asset_dir/carbon-$version-macos-$release_arch-cli.tar.gz" \
  -C "$cli_stage" carbon LICENSE.txt README.txt

assets=("$asset_dir/"*)
if [[ "${#assets[@]}" -ne 2 ]]; then
  echo "macOS release directory must contain exactly two files." >&2
  exit 1
fi
for asset in "${assets[@]}"; do
  if [[ ! -s "$asset" ]]; then
    echo "Empty macOS release asset: $asset" >&2
    exit 1
  fi
done

printf 'macOS %s release assets: %s\n' "$release_arch" "$asset_dir"
