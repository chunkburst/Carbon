[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][ValidateNotNullOrEmpty()][string]$AssetDirectory,
  [Parameter(Mandatory = $true)][ValidateNotNullOrEmpty()][string]$Version
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-SHA256Hex {
  param([Parameter(Mandatory = $true)][string]$Path)

  $resolvedPath = (Resolve-Path -LiteralPath $Path -ErrorAction Stop).ProviderPath
  $stream = [System.IO.File]::OpenRead($resolvedPath)
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

function Assert-PlainDirectory {
  param([Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)][string]$Label)

  $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
  if (-not $item.PSIsContainer) {
    throw "$Label is not a directory: $Path"
  }
  if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Refusing to use a reparse-point ${Label}: $Path"
  }
}

function Assert-PlainFile {
  param([Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)][string]$Label)

  $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
  if ($item.PSIsContainer) {
    throw "$Label is not a file: $Path"
  }
  if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw "Refusing to use a reparse-point ${Label}: $Path"
  }
  if ($item.Length -le 0) {
    throw "$Label is empty: $Path"
  }
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
    throw "$Label must remain within ${fullParent}: $fullPath"
  }
}

function Get-RequiredAssetPath {
  param(
    [Parameter(Mandatory = $true)][string]$Directory,
    [Parameter(Mandatory = $true)][string]$Name,
    [Parameter(Mandatory = $true)][string]$Label
  )

  $candidate = Join-Path -Path $Directory -ChildPath $Name
  $resolved = (Resolve-Path -LiteralPath $candidate -ErrorAction Stop).ProviderPath
  Assert-ChildPath -Path $resolved -Parent $Directory -Label $Label
  Assert-PlainFile -Path $resolved -Label $Label
  return $resolved
}

function Assert-ExactNameSet {
  param(
    [Parameter(Mandatory = $true)][string[]]$Actual,
    [Parameter(Mandatory = $true)][string[]]$Expected,
    [Parameter(Mandatory = $true)][string]$Label
  )

  $actualSorted = @($Actual | Sort-Object)
  $expectedSorted = @($Expected | Sort-Object)
  if ($actualSorted.Count -ne $expectedSorted.Count) {
    throw "$Label count is invalid. Expected $($expectedSorted.Count), got $($actualSorted.Count)."
  }
  for ($index = 0; $index -lt $expectedSorted.Count; $index++) {
    if (-not [string]::Equals($actualSorted[$index], $expectedSorted[$index], [System.StringComparison]::Ordinal)) {
      throw "$Label has an unexpected name: $($actualSorted -join ', ')"
    }
  }
}

function Assert-ZipContents {
  param(
    [Parameter(Mandatory = $true)][string]$ZipPath,
    [Parameter(Mandatory = $true)][string[]]$ExpectedFileNames,
    [Parameter(Mandatory = $true)][string]$Label,
    [switch]$RequireSingleRootDirectory
  )

  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $forbiddenDirectories = @(
    ".carbon",
    ".cairn",
    ".codex",
    ".codex-tmp",
    ".codex-test-tmp",
    "archives",
    "backups",
    "node_modules",
    "target",
    "release-assets"
  )
  $actualFileNames = @()
  $seenEntries = @{}
  $seenFiles = @{}
  $rootDirectory = $null
  $zip = [System.IO.Compression.ZipFile]::OpenRead($ZipPath)
  try {
    foreach ($entry in $zip.Entries) {
      $entryName = [string]$entry.FullName
      if ([string]::IsNullOrWhiteSpace($entryName)) {
        throw "$Label contains an empty ZIP entry name."
      }
      $normalizedName = $entryName.Replace("\", "/")
      if ($normalizedName -match "^(?:/|[A-Za-z]:/)") {
        throw "$Label contains an absolute ZIP path: $entryName"
      }
      $entryKey = $normalizedName.ToLowerInvariant()
      if ($seenEntries.ContainsKey($entryKey)) {
        throw "$Label contains a duplicate ZIP entry: $entryName"
      }
      $seenEntries[$entryKey] = $true

      $segments = @($normalizedName.Split("/") | Where-Object { $_.Length -gt 0 })
      if ($segments.Count -eq 0) {
        throw "$Label contains an invalid ZIP entry: $entryName"
      }
      foreach ($segment in $segments) {
        if ($segment -eq "." -or $segment -eq "..") {
          throw "$Label contains an unsafe ZIP path: $entryName"
        }
        if ($forbiddenDirectories -contains $segment.ToLowerInvariant()) {
          throw "$Label contains a forbidden data or build directory: $entryName"
        }
      }
      for ($segmentIndex = 0; $segmentIndex -lt ($segments.Count - 1); $segmentIndex++) {
        if (
          $segments[$segmentIndex].Equals("docs", [System.StringComparison]::OrdinalIgnoreCase) -and
          $segments[$segmentIndex + 1].Equals("reports", [System.StringComparison]::OrdinalIgnoreCase)
        ) {
          throw "$Label contains a forbidden docs/reports directory: $entryName"
        }
      }
      if ($entry.Name.Equals(".cairn-cluster.json", [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "$Label contains a forbidden .cairn-cluster.json file."
      }

      $isDirectory = $normalizedName.EndsWith("/")
      if ($RequireSingleRootDirectory) {
        $candidateRoot = $segments[0]
        if ($null -eq $rootDirectory) {
          $rootDirectory = $candidateRoot
        } elseif (-not $rootDirectory.Equals($candidateRoot, [System.StringComparison]::Ordinal)) {
          throw "$Label must use exactly one root directory."
        }
        if ($isDirectory) {
          if ($segments.Count -ne 1) {
            throw "$Label contains a nested directory entry: $entryName"
          }
          continue
        }
        if ($segments.Count -ne 2) {
          throw "$Label must contain files directly below its single root directory: $entryName"
        }
        $canonicalName = $segments[1]
      } else {
        if ($isDirectory -or $segments.Count -ne 1) {
          throw "$Label must contain only root-level files: $entryName"
        }
        $canonicalName = $segments[0]
      }

      if (-not $entry.Name.Equals($canonicalName, [System.StringComparison]::Ordinal)) {
        throw "$Label contains an invalid ZIP entry name: $entryName"
      }
      if ($entry.Length -le 0) {
        throw "$Label contains an empty file: $entryName"
      }
      $fileKey = $canonicalName.ToLowerInvariant()
      if ($seenFiles.ContainsKey($fileKey)) {
        throw "$Label contains a duplicate file: $canonicalName"
      }
      $seenFiles[$fileKey] = $true
      $actualFileNames += $canonicalName
    }
  } finally {
    $zip.Dispose()
  }

  if ($RequireSingleRootDirectory -and $null -eq $rootDirectory) {
    throw "$Label must contain a single root directory."
  }
  Assert-ExactNameSet -Actual $actualFileNames -Expected $ExpectedFileNames -Label "$Label contents"
}

if ($Version -notmatch "^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$") {
  throw "Version must be a stable SemVer value without a v prefix: $Version"
}

$assetDir = (Resolve-Path -LiteralPath $AssetDirectory -ErrorAction Stop).ProviderPath
Assert-PlainDirectory -Path $assetDir -Label "release asset directory"

$requiredAssetNames = @(
  "Carbon_${Version}_x64-setup.exe",
  "Carbon_${Version}_x64_en-US.msi",
  "Carbon-${Version}-windows-portable.zip",
  "carbon-${Version}-windows-x64-cli.zip",
  "SHA256SUMS.txt"
)
$assetChildren = @(Get-ChildItem -LiteralPath $assetDir -Force -ErrorAction Stop)
if ($assetChildren.Count -ne $requiredAssetNames.Count) {
  throw "Release asset directory must contain exactly five top-level assets; found $($assetChildren.Count)."
}
foreach ($child in $assetChildren) {
  if ($child.PSIsContainer) {
    throw "Release asset directory must not contain subdirectories: $($child.FullName)"
  }
  Assert-PlainFile -Path ((Resolve-Path -LiteralPath $child.FullName -ErrorAction Stop).ProviderPath) -Label "release asset"
}
Assert-ExactNameSet -Actual @($assetChildren | ForEach-Object Name) -Expected $requiredAssetNames -Label "Release asset directory"

$installerPath = Get-RequiredAssetPath -Directory $assetDir -Name "Carbon_${Version}_x64-setup.exe" -Label "NSIS installer"
$msiPath = Get-RequiredAssetPath -Directory $assetDir -Name "Carbon_${Version}_x64_en-US.msi" -Label "MSI installer"
$portablePath = Get-RequiredAssetPath -Directory $assetDir -Name "Carbon-${Version}-windows-portable.zip" -Label "portable ZIP"
$cliPath = Get-RequiredAssetPath -Directory $assetDir -Name "carbon-${Version}-windows-x64-cli.zip" -Label "CLI ZIP"
$checksumsPath = Get-RequiredAssetPath -Directory $assetDir -Name "SHA256SUMS.txt" -Label "SHA256SUMS"

$checksumLines = @([System.IO.File]::ReadAllLines($checksumsPath, [System.Text.Encoding]::ASCII))
if ($checksumLines.Count -ne 4) {
  throw "SHA256SUMS.txt must contain exactly four checksum lines; found $($checksumLines.Count)."
}
$checksummedNames = @(
  "Carbon_${Version}_x64-setup.exe",
  "Carbon_${Version}_x64_en-US.msi",
  "Carbon-${Version}-windows-portable.zip",
  "carbon-${Version}-windows-x64-cli.zip"
)
$seenChecksums = @{}
foreach ($line in $checksumLines) {
  $match = [System.Text.RegularExpressions.Regex]::Match($line, "^([0-9a-f]{64})  ([^/\\]+)$")
  if (-not $match.Success) {
    throw "Invalid SHA256SUMS.txt line: $line"
  }
  $expectedHash = $match.Groups[1].Value
  $name = $match.Groups[2].Value
  if ($seenChecksums.ContainsKey($name)) {
    throw "SHA256SUMS.txt contains a duplicate filename: $name"
  }
  if ($checksummedNames -notcontains $name) {
    throw "SHA256SUMS.txt contains an unexpected filename: $name"
  }
  $seenChecksums[$name] = $true
  $actualHash = Get-SHA256Hex -Path (Get-RequiredAssetPath -Directory $assetDir -Name $name -Label "checksummed release asset")
  if (-not [string]::Equals($actualHash, $expectedHash, [System.StringComparison]::Ordinal)) {
    throw "SHA-256 mismatch for $name"
  }
}
Assert-ExactNameSet -Actual @($seenChecksums.Keys) -Expected $checksummedNames -Label "SHA256SUMS.txt"

Assert-ZipContents -ZipPath $portablePath -ExpectedFileNames @(
  "Carbon Portable.exe",
  "carbon.exe",
  "WebView2Loader.dll",
  "carbon-portable.marker",
  "README.txt"
) -Label "portable ZIP" -RequireSingleRootDirectory
Assert-ZipContents -ZipPath $cliPath -ExpectedFileNames @(
  "carbon.exe",
  "LICENSE.txt",
  "README.txt"
) -Label "CLI ZIP"

Write-Host "Verified Windows release assets for Carbon ${Version}: $assetDir"
