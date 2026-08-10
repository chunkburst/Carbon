[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# Build the portable package first. Dot-sourcing intentionally keeps the validated toolchain,
# target paths, and path-safety helpers selected by the portable builder in this scope.
. (Join-Path $PSScriptRoot "build-windows-portable.ps1")

function Get-SHA256Hex {
  param([Parameter(Mandatory = $true)][string]$Path)

  $stream = [System.IO.File]::OpenRead($Path)
  try {
    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
      return ([System.BitConverter]::ToString($sha256.ComputeHash($stream))).Replace("-", "").ToLowerInvariant()
    } finally {
      $sha256.Dispose()
    }
  } finally {
    $stream.Dispose()
  }
}

$bundleArgs = @("build", "--bundles", "nsis,msi", "--ci")
if ($targetTriple) {
  $bundleArgs += @("--target", $targetTriple)
}

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
    $env:CARGO_TARGET_X86_64_PC_WINDOWS_GNU_LINKER = $rustLinker
    $env:CARGO_TARGET_DIR = $cargoTargetDir
    $nativeLinkSearch = "-Lnative=$gnuWebView2Dir"
    $env:RUSTFLAGS = if ([string]::IsNullOrWhiteSpace($previousEnvironment["RUSTFLAGS"])) {
      $nativeLinkSearch
    } else {
      "$($previousEnvironment["RUSTFLAGS"]) $nativeLinkSearch"
    }
    if ($null -ne $previousEnvironment["CARGO_ENCODED_RUSTFLAGS"]) {
      $env:CARGO_ENCODED_RUSTFLAGS = if ([string]::IsNullOrEmpty($previousEnvironment["CARGO_ENCODED_RUSTFLAGS"])) {
        $nativeLinkSearch
      } else {
        "$($previousEnvironment["CARGO_ENCODED_RUSTFLAGS"])$([char]0x1F)$nativeLinkSearch"
      }
    }
    & pnpm.cmd --dir $desktopDir exec tauri @bundleArgs
    $bundleExitCode = $LASTEXITCODE
  } finally {
    foreach ($environmentName in $previousEnvironment.Keys) {
      [Environment]::SetEnvironmentVariable($environmentName, $previousEnvironment[$environmentName], [System.EnvironmentVariableTarget]::Process)
    }
  }
} else {
  & pnpm.cmd --dir $desktopDir exec tauri @bundleArgs
  $bundleExitCode = $LASTEXITCODE
}
if ($bundleExitCode -ne 0) {
  throw "tauri build --bundles nsis,msi failed with exit code $bundleExitCode."
}

$bundleRoot = Join-Path $releaseDir "bundle"
$nsisBundleDir = Assert-ChildPath -Path (Join-Path $bundleRoot "nsis") -Parent $bundleRoot -Label "NSIS bundle directory"
$msiBundleDir = Assert-ChildPath -Path (Join-Path $bundleRoot "msi") -Parent $bundleRoot -Label "MSI bundle directory"
Assert-PlainDirectory -Path $nsisBundleDir -Label "NSIS bundle directory" -InspectChildren
Assert-PlainDirectory -Path $msiBundleDir -Label "MSI bundle directory" -InspectChildren

# Tauri retains prior-version installers in a shared Cargo target directory. Select only the
# two deterministic artifact names for the version declared in tauri.conf.json; do not infer
# correctness from the total number of .exe/.msi files in those directories.
$nsisArtifact = Assert-PlainFile -Path (
  Assert-ChildPath -Path (Join-Path $nsisBundleDir "Carbon_${version}_x64-setup.exe") -Parent $nsisBundleDir -Label "NSIS installer"
) -Label "NSIS installer"
$msiArtifact = Assert-PlainFile -Path (
  Assert-ChildPath -Path (Join-Path $msiBundleDir "Carbon_${version}_x64_en-US.msi") -Parent $msiBundleDir -Label "MSI installer"
) -Label "MSI installer"

$releaseAssetsRoot = Assert-ChildPath -Path (Join-Path $targetDir "release-assets") -Parent $targetDir -Label "release assets"
Assert-PlainDirectory -Path $releaseAssetsRoot -Label "release assets" -Create
$releaseName = "Carbon-$version-windows-x64"
$releaseAssetsDir = Assert-ChildPath -Path (Join-Path $releaseAssetsRoot $releaseName) -Parent $releaseAssetsRoot -Label "versioned release assets"
if (Test-Path -LiteralPath $releaseAssetsDir) {
  Assert-PlainDirectory -Path $releaseAssetsDir -Label "versioned release assets" -InspectChildren
  Remove-Item -LiteralPath $releaseAssetsDir -Recurse -Force
}
New-Item -ItemType Directory -Path $releaseAssetsDir | Out-Null

$portableAsset = Join-Path $releaseAssetsDir (Split-Path -Leaf $zipPath)
$nsisAsset = Join-Path $releaseAssetsDir (Split-Path -Leaf $nsisArtifact)
$msiAsset = Join-Path $releaseAssetsDir (Split-Path -Leaf $msiArtifact)
Copy-Item -LiteralPath $zipPath -Destination $portableAsset
Copy-Item -LiteralPath $nsisArtifact -Destination $nsisAsset
Copy-Item -LiteralPath $msiArtifact -Destination $msiAsset

$cliStage = Assert-ChildPath -Path (Join-Path $releaseAssetsDir ".cli-stage") -Parent $releaseAssetsDir -Label "CLI staging"
New-Item -ItemType Directory -Path $cliStage | Out-Null
Copy-Item -LiteralPath $sidecarExe -Destination (Join-Path $cliStage "carbon.exe")
Copy-Item -LiteralPath (Join-Path $repoRoot "LICENSE") -Destination (Join-Path $cliStage "LICENSE.txt")
$cliReadme = @"
Carbon CLI $version for Windows x64
==================================

Run carbon.exe version to verify the binary, then use carbon.exe home init --home <path>.
This package contains the CLI, local Web server, and MCP server only; it does not include the desktop UI.
"@
Set-Content -LiteralPath (Join-Path $cliStage "README.txt") -Encoding utf8 -Value $cliReadme
$cliAsset = Join-Path $releaseAssetsDir "carbon-$version-windows-x64-cli.zip"
Compress-Archive -Path (Join-Path $cliStage "*") -DestinationPath $cliAsset -CompressionLevel Optimal
Remove-Item -LiteralPath $cliStage -Recurse -Force

$releaseArtifacts = @($nsisAsset, $msiAsset, $portableAsset, $cliAsset)
$checksumLines = foreach ($artifact in $releaseArtifacts | Sort-Object { Split-Path -Leaf $_ }) {
  $hash = Get-SHA256Hex -Path $artifact
  "$hash  $(Split-Path -Leaf $artifact)"
}
$checksumsPath = Join-Path $releaseAssetsDir "SHA256SUMS.txt"
Set-Content -LiteralPath $checksumsPath -Encoding ascii -Value $checksumLines

Write-Host "Windows release assets: $releaseAssetsDir"
foreach ($artifact in @($releaseArtifacts + $checksumsPath)) {
  $item = Get-Item -LiteralPath $artifact
  Write-Host ("  {0} ({1:N0} bytes)" -f $item.Name, $item.Length)
}
