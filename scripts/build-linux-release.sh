#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(cd -- "$script_dir/.." && pwd -P)"

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "Linux release artifacts must be built on Linux." >&2
  exit 1
fi

host_arch="$(uname -m)"
target="${1:-}"
release_arch="${2:-}"
if [[ -z "$target" || -z "$release_arch" ]]; then
  case "$host_arch" in
    aarch64)
      target="aarch64-unknown-linux-gnu"
      release_arch="arm64"
      ;;
    x86_64)
      target="x86_64-unknown-linux-gnu"
      release_arch="x64"
      ;;
    *)
      echo "Unsupported Linux host architecture: $host_arch" >&2
      exit 1
      ;;
  esac
fi

case "$target:$release_arch:$host_arch" in
  aarch64-unknown-linux-gnu:arm64:aarch64)
    expected_deb_arch="arm64"
    expected_machine='AArch64'
    ;;
  x86_64-unknown-linux-gnu:x64:x86_64)
    expected_deb_arch="amd64"
    expected_machine='Advanced Micro Devices X86-64'
    ;;
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

asset_dir="$repo_root/desktop/src-tauri/target/release-assets/Carbon-$version-linux-$release_arch"
bundle_root="$repo_root/desktop/src-tauri/target/$target/release/bundle"
rm -rf -- "$asset_dir"
mkdir -p -- "$asset_dir"

export TARGET_TRIPLE="$target"
pnpm --dir "$repo_root/desktop" exec tauri build --bundles deb,appimage --target "$target"

mapfile -t deb_files < <(find "$bundle_root/deb" -mindepth 1 -maxdepth 1 -type f -name '*.deb' -print | LC_ALL=C sort)
mapfile -t appimage_files < <(find "$bundle_root/appimage" -mindepth 1 -maxdepth 1 -type f -name '*.AppImage' -print | LC_ALL=C sort)
if [[ "${#deb_files[@]}" -ne 1 || "${#appimage_files[@]}" -ne 1 ]]; then
  echo "Expected exactly one DEB and one AppImage." >&2
  printf 'DEB=%s AppImage=%s\n' "${#deb_files[@]}" "${#appimage_files[@]}" >&2
  exit 1
fi

assert_elf_arch() {
  local binary="$1"
  if [[ ! -f "$binary" ]]; then
    echo "Expected ELF executable is missing: $binary" >&2
    exit 1
  fi
  if ! readelf -h "$binary" | grep -Fq "Machine:                           $expected_machine"; then
    echo "Unexpected ELF architecture: $binary" >&2
    readelf -h "$binary" >&2 || true
    exit 1
  fi
}

sidecar_source="$repo_root/desktop/src-tauri/binaries/carbon-$target"
assert_elf_arch "$sidecar_source"
cli_version="$("$sidecar_source" version)"
if [[ "$cli_version" != "Carbon $version" ]]; then
  echo "Unexpected Carbon CLI version: $cli_version" >&2
  exit 1
fi

deb_arch="$(dpkg-deb -f "${deb_files[0]}" Architecture)"
if [[ "$deb_arch" != "$expected_deb_arch" ]]; then
  echo "Unexpected DEB architecture: $deb_arch (expected $expected_deb_arch)." >&2
  exit 1
fi

inspect_root="$(mktemp -d "${TMPDIR:-/tmp}/carbon-linux-release.XXXXXX")"
cleanup() {
  rm -rf -- "$inspect_root"
}
trap cleanup EXIT
mkdir -p "$inspect_root/deb" "$inspect_root/appimage"
dpkg-deb -x "${deb_files[0]}" "$inspect_root/deb"
mapfile -t deb_sidecars < <(find "$inspect_root/deb" -type f -name carbon -print)
if [[ "${#deb_sidecars[@]}" -ne 1 ]]; then
  echo "Expected exactly one Carbon sidecar in the DEB, found ${#deb_sidecars[@]}." >&2
  exit 1
fi
assert_elf_arch "${deb_sidecars[0]}"

chmod +x "${appimage_files[0]}"
(
  cd "$inspect_root/appimage"
  "${appimage_files[0]}" --appimage-extract >/dev/null
)
mapfile -t appimage_sidecars < <(find "$inspect_root/appimage/squashfs-root" -type f -name carbon -print)
if [[ "${#appimage_sidecars[@]}" -ne 1 ]]; then
  echo "Expected exactly one Carbon sidecar in the AppImage, found ${#appimage_sidecars[@]}." >&2
  exit 1
fi
assert_elf_arch "${appimage_sidecars[0]}"

cp -p -- "${deb_files[0]}" "$asset_dir/Carbon-$version-linux-$release_arch.deb"
cp -p -- "${appimage_files[0]}" "$asset_dir/Carbon-$version-linux-$release_arch.AppImage"
chmod 0755 "$asset_dir/Carbon-$version-linux-$release_arch.AppImage"

cli_stage="$inspect_root/cli"
mkdir -p "$cli_stage"
cp -p -- "$sidecar_source" "$cli_stage/carbon"
cp -p -- "$repo_root/LICENSE" "$cli_stage/LICENSE.txt"
printf '%s\n' \
  "Carbon $version CLI for Linux $release_arch" \
  "" \
  "Run ./carbon version, then ./carbon --help." \
  "This archive contains the CLI/Web/MCP sidecar only; use the AppImage or DEB for the desktop app." \
  > "$cli_stage/README.txt"
chmod 0755 "$cli_stage/carbon"
tar -czf "$asset_dir/carbon-$version-linux-$release_arch-cli.tar.gz" \
  -C "$cli_stage" carbon LICENSE.txt README.txt

mapfile -t assets < <(find "$asset_dir" -mindepth 1 -maxdepth 1 -type f -print | LC_ALL=C sort)
if [[ "${#assets[@]}" -ne 3 ]]; then
  echo "Linux release directory must contain exactly three files." >&2
  exit 1
fi
for asset in "${assets[@]}"; do
  if [[ ! -s "$asset" ]]; then
    echo "Empty Linux release asset: $asset" >&2
    exit 1
  fi
done

printf 'Linux %s release assets: %s\n' "$release_arch" "$asset_dir"
