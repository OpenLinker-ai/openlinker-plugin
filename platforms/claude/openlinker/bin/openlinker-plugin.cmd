@echo off
setlocal
set "PLUGIN_DIR=%~dp0.."
set "CLI_PATH="
for /f "usebackq delims=" %%I in (`powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%PLUGIN_DIR%\scripts\resolve-openlinker-cli.ps1" -Require plugin.serve`) do set "CLI_PATH=%%I"
if errorlevel 1 exit /b %errorlevel%
if not defined CLI_PATH (
  echo openlinker plugin: resolver returned no CLI path 1>&2
  exit /b 4
)
"%CLI_PATH%" %*
exit /b %errorlevel%
