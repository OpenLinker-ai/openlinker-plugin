[CmdletBinding()]
param(
    [Parameter()]
    [switch] $Plan
)

$ErrorActionPreference = "Stop"
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$installer = Join-Path $scriptDir "install-openlinker-cli.mjs"
$arguments = @($installer)
if ($Plan) {
    $arguments += "--plan"
}
& node @arguments
exit $LASTEXITCODE
