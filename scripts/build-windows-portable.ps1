[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
  throw "The Windows portable package can only be built on Windows."
}

function Assert-PlainDirectory {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][string]$Label,
    [switch]$Create,
    [switch]$InspectChildren
  )

  if (-not (Test-Path -LiteralPath $Path)) {
    if (-not $Create) {
      throw "$Label directory is missing: $Path"
    }
    New-Item -ItemType Directory -Path $Path | Out-Null
  }
  if (-not (Test-Path -LiteralPath $Path -PathType Container)) {
    throw "$Label path is not a directory: $Path"
  }

  $item = Get-Item -LiteralPath $Path -Force
  if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Refusing to use a reparse-point $Label directory: $Path"
  }
  if ($InspectChildren) {
    $reparsePoint = Get-ChildItem -LiteralPath $Path -Force -Recurse -ErrorAction Stop |
      Where-Object { ($_.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 } |
      Select-Object -First 1
    if ($reparsePoint) {
      throw "Refusing to build through a reparse point inside the $Label directory: $($reparsePoint.FullName)"
    }
  }
}

function Assert-PlainFile {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
    throw "$Label file is missing: $Path"
  }

  $item = Get-Item -LiteralPath $Path -Force
  if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Refusing to use a reparse-point $Label file: $Path"
  }

  return [System.IO.Path]::GetFullPath($item.FullName)
}

function Assert-ChildPath {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][string]$Parent,
    [Parameter(Mandatory = $true)][string]$Label
  )

  $fullParent = [System.IO.Path]::GetFullPath($Parent)
  $fullPath = [System.IO.Path]::GetFullPath($Path)
  $separator = [System.IO.Path]::DirectorySeparatorChar
  $parentPrefix = if ($fullParent.EndsWith($separator)) { $fullParent } else { "$fullParent$separator" }
  if (-not $fullPath.StartsWith($parentPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "$Label path must remain within ${fullParent}: $fullPath"
  }

  return $fullPath
}

function Assert-AsciiCargoTargetRoot {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][string]$Label,
    [switch]$Create
  )

  if (-not [System.IO.Path]::IsPathRooted($Path)) {
    throw "$Label must be an absolute directory path: $Path"
  }
  $fullRoot = [System.IO.Path]::GetFullPath($Path)
  # RUSTFLAGS is space-delimited, so the Cargo target root needs both ASCII and no whitespace.
  if ($fullRoot -match "[^\x00-\x7F]" -or $fullRoot -match "\s") {
    throw "$Label must be an ASCII path without spaces: $fullRoot"
  }
  Assert-PlainDirectory -Path $fullRoot -Label $Label -Create:$Create
  return $fullRoot
}

function Get-AsciiBuildDirectory {
  param(
    [Parameter(Mandatory = $true)][string]$DirectoryName,
    [string]$Root
  )

  if (-not [string]::IsNullOrWhiteSpace($Root)) {
    # CARBON_BUILD_TEMP_ROOT is an operator-selected, persistent build volume. Validate the
    # root itself after creating it, then constrain the generated Cargo target to that exact
    # directory before inspecting it for reparse points.
    $fullRoot = Assert-AsciiCargoTargetRoot -Path $Root -Label "CARBON_BUILD_TEMP_ROOT" -Create
    $buildDirectory = Assert-ChildPath -Path (Join-Path $fullRoot $DirectoryName) -Parent $fullRoot -Label "Cargo target"
    Assert-PlainDirectory -Path $buildDirectory -Label "Cargo target" -Create -InspectChildren
    return $buildDirectory
  }

  # Without an explicit build volume, use only the process system TEMP. Do not silently spill
  # into other shared directories, because callers can use CARBON_BUILD_TEMP_ROOT to keep the
  # multi-gigabyte Cargo target on a dedicated drive.
  $systemTemp = [System.IO.Path]::GetTempPath()
  try {
    $fullRoot = Assert-AsciiCargoTargetRoot -Path $systemTemp -Label "system TEMP" -Create
    $buildDirectory = Assert-ChildPath -Path (Join-Path $fullRoot $DirectoryName) -Parent $fullRoot -Label "Cargo target"
    Assert-PlainDirectory -Path $buildDirectory -Label "Cargo target" -Create -InspectChildren
    return $buildDirectory
  } catch {
    throw "The GNU build requires a writable, non-reparse ASCII path without spaces for CARGO_TARGET_DIR. Set CARBON_BUILD_TEMP_ROOT to an absolute path. System TEMP failed: $($_.Exception.Message)"
  }
}

function Remove-PlainFileIfPresent {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][string]$Label
  )

  if (Test-Path -LiteralPath $Path) {
    $null = Assert-PlainFile -Path $Path -Label $Label
    Remove-Item -LiteralPath $Path -Force
  }
}

$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$desktopDir = Join-Path $repoRoot "desktop"
$tauriDir = Join-Path $desktopDir "src-tauri"
$targetDir = [System.IO.Path]::GetFullPath((Join-Path $tauriDir "target"))
Assert-PlainDirectory -Path $repoRoot -Label "repository"
Assert-PlainDirectory -Path $desktopDir -Label "desktop"
Assert-PlainDirectory -Path $tauriDir -Label "Tauri"
Assert-PlainDirectory -Path $targetDir -Label "Tauri target" -Create -InspectChildren
$configPath = Join-Path $tauriDir "tauri.conf.json"
$cargoManifestPath = Join-Path $tauriDir "Cargo.toml"
$null = Assert-PlainFile -Path $configPath -Label "Tauri configuration"
$null = Assert-PlainFile -Path $cargoManifestPath -Label "Tauri Cargo manifest"
$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
$version = [string]$config.version

if ([string]::IsNullOrWhiteSpace($version)) {
  throw "desktop/src-tauri/tauri.conf.json must contain a version."
}

# Fetching first makes the metadata-selected registry source available on a clean build host.
& cargo fetch --locked --manifest-path $cargoManifestPath
if ($LASTEXITCODE -ne 0) {
  throw "cargo fetch --locked failed with exit code $LASTEXITCODE."
}

$cargoMetadataJson = (& cargo metadata --locked --format-version 1 --manifest-path $cargoManifestPath | Out-String)
if ($LASTEXITCODE -ne 0) {
  throw "cargo metadata --locked failed with exit code $LASTEXITCODE."
}
try {
  $cargoMetadata = $cargoMetadataJson | ConvertFrom-Json
} catch {
  throw "cargo metadata returned invalid JSON: $($_.Exception.Message)"
}

$webView2Packages = @($cargoMetadata.packages | Where-Object { $_.name -eq "webview2-com-sys" })
if ($webView2Packages.Count -ne 1) {
  throw "Cargo.lock must resolve exactly one webview2-com-sys package; cargo metadata found $($webView2Packages.Count)."
}
$webView2ManifestPath = [System.IO.Path]::GetFullPath([string]$webView2Packages[0].manifest_path)
$null = Assert-PlainFile -Path $webView2ManifestPath -Label "webview2-com-sys manifest"
$webView2CrateDir = Split-Path -Parent $webView2ManifestPath
Assert-PlainDirectory -Path $webView2CrateDir -Label "webview2-com-sys source"
$webView2LoaderDll = Assert-ChildPath -Path (Join-Path $webView2CrateDir "x64\WebView2Loader.dll") -Parent $webView2CrateDir -Label "WebView2Loader source"
$null = Assert-PlainFile -Path $webView2LoaderDll -Label "WebView2Loader source"

# Prefer the officially supported MSVC toolchain. A user-level MinGW/Rust GNU installation
# is a practical fallback on machines without Visual Studio Build Tools (such as clean
# portable-build hosts). Both produce the same two-process application layout.
$targetTriple = $null
$rustToolchain = $null
$rustLinker = $null
$cargoTargetDir = $null
$gnuWebView2Dir = $null
$tauriArgs = @("build", "--no-bundle")
$msvcLinkCommand = Get-Command link.exe -ErrorAction SilentlyContinue
$msvcLinkReady = $false
if ($msvcLinkCommand) {
  # Git for Windows also ships a POSIX `link.exe`. Its presence must not select
  # Rust's MSVC target: it cannot understand LINK.EXE arguments and paths passed
  # through Git Bash may also be recoded. Trust only Microsoft's signed version
  # metadata; probing the POSIX binary writes a native stderr record that becomes
  # terminating under this script's strict error policy.
  $msvcLinkPath = Assert-PlainFile -Path $msvcLinkCommand.Source -Label "candidate MSVC linker"
  $msvcLinkVersion = (Get-Item -LiteralPath $msvcLinkPath -Force).VersionInfo
  $msvcLinkReady =
    $msvcLinkVersion.CompanyName -match "(?i)^Microsoft" -and
    $msvcLinkVersion.OriginalFilename -match "(?i)^link\.exe$"
}
if (-not $msvcLinkReady) {
  $gnuTarget = "x86_64-pc-windows-gnu"
  $gnuToolchain = "stable-$gnuTarget"
  # Inspect both the persisted user PATH and this process' inherited PATH. This avoids
  # accidentally selecting a stale MinGW installation from an older terminal session.
  $candidatePathEntries = @(
    ([Environment]::GetEnvironmentVariable("Path", "User") -split ";")
    ($env:Path -split ";")
  ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -Unique
  $gnuLinkerCandidates = @(
    foreach ($pathEntry in $candidatePathEntries) {
      $expandedPathEntry = [Environment]::ExpandEnvironmentVariables($pathEntry)
      foreach ($linkerName in @("gcc.exe", "x86_64-w64-mingw32-gcc.exe")) {
        $candidate = Join-Path $expandedPathEntry $linkerName
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
          [System.IO.Path]::GetFullPath($candidate)
        }
      }
    }
  ) | Select-Object -Unique

  $rustLinker = $null
  foreach ($candidate in $gnuLinkerCandidates) {
    $libgccEh = [string](& $candidate "-print-file-name=libgcc_eh.a")
    $candidateLd = [string](& $candidate "-print-prog-name=ld")
    if (
      (Test-Path -LiteralPath $libgccEh -PathType Leaf) -and
      (Test-Path -LiteralPath $candidateLd -PathType Leaf) -and
      ((& $candidateLd --help 2>$null | Out-String) -match "high-entropy-va")
    ) {
      $rustLinker = $candidate
      break
    }
  }

  $gnuBinDir = if ($rustLinker) { Split-Path -Parent $rustLinker } else { $null }
  $gnuCpp = if ($gnuBinDir) {
    @("g++.exe", "x86_64-w64-mingw32-g++.exe") |
      ForEach-Object { Join-Path $gnuBinDir $_ } |
      Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
      Select-Object -First 1
  }
  $gnuWindres = if ($gnuBinDir) {
    @("windres.exe", "x86_64-w64-mingw32-windres.exe") |
      ForEach-Object { Join-Path $gnuBinDir $_ } |
      Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
      Select-Object -First 1
  }
  $gnuGenDef = if ($gnuBinDir) { Join-Path $gnuBinDir "gendef.exe" } else { $null }
  $gnuDllTool = if ($gnuBinDir) {
    @("x86_64-w64-mingw32-dlltool.exe", "dlltool.exe") |
      ForEach-Object { Join-Path $gnuBinDir $_ } |
      Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
      Select-Object -First 1
  }
  $missingTools = @()
  if (-not $gnuCpp) { $missingTools += "g++.exe" }
  if (-not $gnuWindres) { $missingTools += "windres.exe" }
  if (-not $gnuGenDef -or -not (Test-Path -LiteralPath $gnuGenDef -PathType Leaf)) { $missingTools += "gendef.exe" }
  if (-not $gnuDllTool) { $missingTools += "dlltool.exe" }
  $installedToolchains = @(& rustup toolchain list | ForEach-Object { ($_ -split "\s+")[0] })
  if (-not $rustLinker -or $missingTools.Count -gt 0 -or $installedToolchains -notcontains $gnuToolchain) {
    $missingToolText = if ($missingTools.Count -gt 0) { " Missing MinGW tools: $($missingTools -join ', ')." } else { "" }
    throw "No Windows Rust linker is ready. Install Visual Studio Build Tools (Desktop development with C++), or install MinGW plus 'rustup toolchain install $gnuToolchain'.$missingToolText"
  }
  $null = Assert-PlainFile -Path $rustLinker -Label "MinGW Rust linker"
  $null = Assert-PlainFile -Path $gnuCpp -Label "MinGW C++ compiler"
  $null = Assert-PlainFile -Path $gnuWindres -Label "MinGW windres"
  $null = Assert-PlainFile -Path $gnuGenDef -Label "MinGW gendef"
  $null = Assert-PlainFile -Path $gnuDllTool -Label "MinGW dlltool"
  $targetTriple = $gnuTarget
  $rustToolchain = $gnuToolchain
  $sha256 = [System.Security.Cryptography.SHA256]::Create()
  try {
    $repoHashBytes = $sha256.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($repoRoot))
  } finally {
    $sha256.Dispose()
  }
  $repoHash = -join ($repoHashBytes[0..7] | ForEach-Object { $_.ToString("x2") })
  # Keep all GNU intermediates out of the source tree, which may contain non-ASCII paths.
  $cargoTargetDir = Get-AsciiBuildDirectory -DirectoryName "carbon-tauri-$repoHash" -Root $env:CARBON_BUILD_TEMP_ROOT
  $gnuWebView2Dir = Assert-ChildPath -Path (Join-Path $cargoTargetDir "gnu-webview2") -Parent $cargoTargetDir -Label "GNU WebView2 support"
  Assert-PlainDirectory -Path $gnuWebView2Dir -Label "GNU WebView2 support" -Create -InspectChildren

  # webview2-com-sys ships a Microsoft import library. GNU ld needs its own import archive,
  # so regenerate it from the exact locked x64 loader DLL in an ASCII-only target subdirectory.
  $gnuLoaderDll = Join-Path $gnuWebView2Dir "WebView2Loader.dll"
  $gnuLoaderDef = Join-Path $gnuWebView2Dir "WebView2Loader.def"
  $gnuLoaderImportLib = Join-Path $gnuWebView2Dir "libWebView2Loader.dll.a"
  foreach ($staleFile in @($gnuLoaderDll, $gnuLoaderDef, $gnuLoaderImportLib)) {
    Remove-PlainFileIfPresent -Path $staleFile -Label "GNU WebView2 intermediate"
  }
  Copy-Item -LiteralPath $webView2LoaderDll -Destination $gnuLoaderDll -Force
  $null = Assert-PlainFile -Path $gnuLoaderDll -Label "GNU WebView2 loader copy"
  # gendef names its .def after the input file in the current directory even when the
  # input is absolute. Keep that output inside the controlled ASCII support directory.
  Push-Location -LiteralPath $gnuWebView2Dir
  try {
    & $gnuGenDef "WebView2Loader.dll"
    if ($LASTEXITCODE -ne 0) {
      throw "gendef failed while reading the locked WebView2Loader.dll (exit code $LASTEXITCODE)."
    }
  } finally {
    Pop-Location
  }
  $null = Assert-PlainFile -Path $gnuLoaderDef -Label "GNU WebView2 definition"
  & $gnuDllTool --machine "i386:x86-64" --input-def $gnuLoaderDef --dllname "WebView2Loader.dll" --output-lib $gnuLoaderImportLib --deterministic-libraries
  if ($LASTEXITCODE -ne 0) {
    throw "dlltool failed while generating libWebView2Loader.dll.a (exit code $LASTEXITCODE)."
  }
  $null = Assert-PlainFile -Path $gnuLoaderImportLib -Label "GNU WebView2 import library"
  $identifiedLoader = (& $gnuDllTool --identify $gnuLoaderImportLib | Out-String).Trim()
  if ($LASTEXITCODE -ne 0 -or $identifiedLoader -notmatch "(?i)^WebView2Loader\.dll$") {
    throw "Generated GNU WebView2 import library does not identify as WebView2Loader.dll: $identifiedLoader"
  }
  $tauriArgs += @("--target", $targetTriple)
}

# `--no-bundle` produces the raw release layout used here: the Tauri executable and the
# external sidecar are placed beside each other. Do not use pnpm.ps1 here: this script is
# intended to work from Windows PowerShell with its default execution policy.
if ($rustToolchain) {
  $previousEnvironment = @{}
  foreach ($environmentName in @(
    "RUSTUP_TOOLCHAIN",
    "CARGO_TARGET_X86_64_PC_WINDOWS_GNU_LINKER",
    "CARGO_TARGET_DIR",
    "RUSTFLAGS",
    "CARGO_ENCODED_RUSTFLAGS"
  )) {
    $previousEnvironment[$environmentName] = [Environment]::GetEnvironmentVariable($environmentName, [System.EnvironmentVariableTarget]::Process)
  }
  try {
    $env:RUSTUP_TOOLCHAIN = $rustToolchain
    # Rust otherwise searches for x86_64-w64-mingw32-gcc.exe, which may resolve to an
    # unrelated legacy MinGW installation even when a current gcc.exe is first in PATH.
    $env:CARGO_TARGET_X86_64_PC_WINDOWS_GNU_LINKER = $rustLinker
    $env:CARGO_TARGET_DIR = $cargoTargetDir
    $nativeLinkSearch = "-Lnative=$gnuWebView2Dir"
    $env:RUSTFLAGS = if ([string]::IsNullOrWhiteSpace($previousEnvironment["RUSTFLAGS"])) {
      $nativeLinkSearch
    } else {
      "$($previousEnvironment["RUSTFLAGS"]) $nativeLinkSearch"
    }
    # Cargo gives CARGO_ENCODED_RUSTFLAGS precedence over RUSTFLAGS. Preserve that behavior
    # while adding the same search path when callers use the encoded form.
    if ($null -ne $previousEnvironment["CARGO_ENCODED_RUSTFLAGS"]) {
      $env:CARGO_ENCODED_RUSTFLAGS = if ([string]::IsNullOrEmpty($previousEnvironment["CARGO_ENCODED_RUSTFLAGS"])) {
        $nativeLinkSearch
      } else {
        "$($previousEnvironment["CARGO_ENCODED_RUSTFLAGS"])$([char]0x1F)$nativeLinkSearch"
      }
    }
    & pnpm.cmd --dir $desktopDir exec tauri @tauriArgs
    $tauriExitCode = $LASTEXITCODE
  } finally {
    foreach ($environmentName in $previousEnvironment.Keys) {
      [Environment]::SetEnvironmentVariable($environmentName, $previousEnvironment[$environmentName], [System.EnvironmentVariableTarget]::Process)
    }
  }
} else {
  & pnpm.cmd --dir $desktopDir exec tauri @tauriArgs
  $tauriExitCode = $LASTEXITCODE
}
if ($tauriExitCode -ne 0) {
  throw "tauri build --no-bundle failed with exit code $tauriExitCode."
}

$releaseDir = if ($cargoTargetDir) {
  Join-Path $cargoTargetDir "$targetTriple\release"
} elseif ($targetTriple) {
  Join-Path $tauriDir "target\$targetTriple\release"
} else {
  Join-Path $tauriDir "target\release"
}
Assert-PlainDirectory -Path $releaseDir -Label "raw Tauri release" -InspectChildren
$mainExe = Join-Path $releaseDir "carbon-desktop.exe"
$sidecarExe = Join-Path $releaseDir "carbon.exe"

foreach ($requiredFile in @($mainExe, $sidecarExe)) {
  $null = Assert-PlainFile -Path $requiredFile -Label "raw Tauri build artifact"
}

$portableRoot = [System.IO.Path]::GetFullPath((Join-Path $targetDir "portable"))
$portableName = "Carbon-$version-windows-portable"
$portableDir = [System.IO.Path]::GetFullPath((Join-Path $portableRoot $portableName))
$zipPath = [System.IO.Path]::GetFullPath((Join-Path $portableRoot "$portableName.zip"))
$separator = [System.IO.Path]::DirectorySeparatorChar
$targetPrefix = "$targetDir$separator"
$portablePrefix = "$portableRoot$separator"

if (
  -not $portableRoot.StartsWith($targetPrefix, [System.StringComparison]::OrdinalIgnoreCase) -or
  -not $portableDir.StartsWith($portablePrefix, [System.StringComparison]::OrdinalIgnoreCase) -or
  -not $zipPath.StartsWith($portablePrefix, [System.StringComparison]::OrdinalIgnoreCase)
) {
  throw "Portable output paths must remain within $targetDir\\portable."
}

if (Test-Path -LiteralPath $portableRoot) {
  $portableRootItem = Get-Item -LiteralPath $portableRoot -Force
  if (-not $portableRootItem.PSIsContainer) {
    throw "Portable output root is not a directory: $portableRoot"
  }
  if (($portableRootItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Refusing to package through a reparse-point portable root: $portableRoot"
  }
}

New-Item -ItemType Directory -Path $portableRoot -Force | Out-Null
if (Test-Path -LiteralPath $portableDir) {
  $portableDirItem = Get-Item -LiteralPath $portableDir -Force
  if (-not $portableDirItem.PSIsContainer) {
    throw "Portable output directory is not a directory: $portableDir"
  }
  if (($portableDirItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Refusing to recursively remove a reparse point: $portableDir"
  }
  Remove-Item -LiteralPath $portableDir -Recurse -Force
}
if (Test-Path -LiteralPath $zipPath) {
  $zipItem = Get-Item -LiteralPath $zipPath -Force
  if ($zipItem.PSIsContainer) {
    throw "Portable ZIP path is a directory: $zipPath"
  }
  if (($zipItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Refusing to overwrite a reparse point: $zipPath"
  }
  Remove-Item -LiteralPath $zipPath -Force
}
New-Item -ItemType Directory -Path $portableDir | Out-Null

# The main executable may be renamed safely: Tauri resolves the sidecar relative to the
# current executable directory, while the sidecar itself must remain carbon.exe.
Copy-Item -LiteralPath $mainExe -Destination (Join-Path $portableDir "Carbon Portable.exe")
Copy-Item -LiteralPath $sidecarExe -Destination (Join-Path $portableDir "carbon.exe")

# Carry any runtime DLLs emitted alongside the raw executable, then force the exact loader
# selected from Cargo metadata into the portable directory rather than relying on a prior
# dependency build output to have copied it beside the executable.
Get-ChildItem -LiteralPath $releaseDir -File -Filter "*.dll" |
  ForEach-Object {
    $null = Assert-PlainFile -Path $_.FullName -Label "runtime DLL"
    Copy-Item -LiteralPath $_.FullName -Destination $portableDir -Force
  }
$portableWebView2Loader = Join-Path $portableDir "WebView2Loader.dll"
Copy-Item -LiteralPath $webView2LoaderDll -Destination $portableWebView2Loader -Force
$null = Assert-PlainFile -Path $portableWebView2Loader -Label "portable WebView2Loader"

Set-Content -LiteralPath (Join-Path $portableDir "carbon-portable.marker") -Encoding ascii -NoNewline -Value "Portable Carbon build: installer updates and deep-link registration are disabled."

$readme = @"
Carbon Portable $version
=======================

Start Carbon Portable.exe.

Keep Carbon Portable.exe and carbon.exe in this same folder. The first is the Carbon desktop UI and tray application; carbon.exe is its local server sidecar.

The local server starts on 127.0.0.1:2525 (or the next free loopback port if 2525 is already busy). It is not exposed to the network by default.

This package requires the Microsoft Edge WebView2 Runtime installed by Windows or your organization. It does not automatically update and does not register the carbon:// deep-link protocol. To update, archive the old files and replace them with a newer portable build.

Language can be switched between Chinese and English in Settings. Launch-at-login supports Off and Standard modes. Administrator mode requires UAC, runs the whole Carbon application with elevated privileges, and is allowed only from a protected application directory.

By default, Carbon Portable uses this executable's folder as its home. Carbon stores central app data in <home>/.carbon (next to this executable by default).

This is a no-installer portable build. Windows/WebView2 may still keep UI cache, logs, and window settings under your user AppData folder.
"@
Set-Content -LiteralPath (Join-Path $portableDir "README.txt") -Encoding utf8 -Value $readme

Compress-Archive -LiteralPath $portableDir -DestinationPath $zipPath -CompressionLevel Optimal

Write-Host "Portable directory: $portableDir"
Write-Host "Portable ZIP:       $zipPath"
