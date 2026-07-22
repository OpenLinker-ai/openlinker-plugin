[CmdletBinding()]
param(
    [Parameter()]
    [string[]] $Require = @()
)

$ErrorActionPreference = "Stop"

$candidate = $null
$sourceName = $null
if (-not [string]::IsNullOrWhiteSpace($env:OPENLINKER_CLI_BIN)) {
    $candidate = $env:OPENLINKER_CLI_BIN
    $sourceName = "OPENLINKER_CLI_BIN"
} else {
    $command = Get-Command openlinker.exe -ErrorAction SilentlyContinue
    if ($null -eq $command) {
        $command = Get-Command openlinker -ErrorAction SilentlyContinue
    }
    if ($null -ne $command) {
        $candidate = $command.Source
        $sourceName = "PATH"
    } else {
        $pluginData = $env:OPENLINKER_PLUGIN_DATA
        if ([string]::IsNullOrWhiteSpace($pluginData)) {
            $pluginData = $env:PLUGIN_DATA
        }
        if ([string]::IsNullOrWhiteSpace($pluginData)) {
            $pluginData = $env:CLAUDE_PLUGIN_DATA
        }
        if ([string]::IsNullOrWhiteSpace($pluginData)) {
            if (-not [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
                $pluginData = Join-Path $env:LOCALAPPDATA "OpenLinker/Plugin"
            } elseif (-not [string]::IsNullOrWhiteSpace($env:USERPROFILE)) {
                $pluginData = Join-Path $env:USERPROFILE "AppData/Local/OpenLinker/Plugin"
            }
        }
        if (-not [string]::IsNullOrWhiteSpace($pluginData)) {
            $privateCLI = Join-Path $pluginData "bin/openlinker.exe"
            if (Test-Path -LiteralPath $privateCLI -PathType Leaf) {
                $candidate = $privateCLI
                $sourceName = "plugin-data"
            }
        }
    }
}

if ([string]::IsNullOrWhiteSpace($candidate)) {
    [Console]::Error.WriteLine("openlinker plugin: compatible CLI not found; run the setup-openlinker-cli skill")
    exit 3
}
if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) {
    [Console]::Error.WriteLine("openlinker plugin: $sourceName does not point to an executable regular file")
    exit 3
}

$stderrPath = Join-Path ([System.IO.Path]::GetTempPath()) ("openlinker-cli-check-" + [guid]::NewGuid().ToString("N") + ".err")
try {
    $contextRaw = (& $candidate context 2>$stderrPath | Out-String)
    if ($LASTEXITCODE -ne 0) {
        [Console]::Error.WriteLine("openlinker plugin: CLI context check failed for $sourceName")
        if (Test-Path -LiteralPath $stderrPath) {
            Get-Content -LiteralPath $stderrPath -TotalCount 5 | ForEach-Object { [Console]::Error.WriteLine($_) }
        }
        exit 4
    }
    try {
        $context = $contextRaw | ConvertFrom-Json
    } catch {
        [Console]::Error.WriteLine("openlinker plugin: CLI context output is not valid JSON")
        exit 4
    }
} finally {
    Remove-Item -LiteralPath $stderrPath -Force -ErrorAction SilentlyContinue
}

if ($context.surface_version -ne "openlinker.cli.v1") {
    [Console]::Error.WriteLine("openlinker plugin: CLI does not expose surface openlinker.cli.v1")
    exit 4
}

$version = [string]$context.cli_version
$compatible = $false
if ($version -eq "dev" -and $env:OPENLINKER_ALLOW_DEV_CLI -eq "true") {
    $compatible = $true
} elseif ($version -match '^v?0\.2\.(?<patch>[0-9]+)(?:-(?<pre>[^+]+))?(?:\+.*)?$') {
    $patch = [int]$Matches.patch
    $pre = [string]$Matches.pre
    if ($patch -gt 0 -or [string]::IsNullOrEmpty($pre)) {
        $compatible = $true
    } elseif ($pre -match '^rc\.(?<rc>[0-9]+)$' -and [int]$Matches.rc -ge 1) {
        $compatible = $true
    }
}
if (-not $compatible) {
    [Console]::Error.WriteLine("openlinker plugin: CLI version $version is outside >=0.2.0-rc.1 <0.3.0")
    exit 4
}

$capabilities = @($context.capabilities)
foreach ($capability in $Require) {
    if ($capabilities -notcontains $capability) {
        [Console]::Error.WriteLine("openlinker plugin: CLI capability missing: $capability")
        exit 5
    }
}

Write-Output ([System.IO.Path]::GetFullPath($candidate))
