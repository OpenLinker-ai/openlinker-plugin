@echo off
setlocal
set "PLUGIN_DIR=%~dp0.."
set "CLI_PATH="
set "REQUIRED_CAPABILITY=plugin.serve"
if "%~1"=="plugin" if "%~2"=="browser-serve" set "REQUIRED_CAPABILITY=plugin.browser.serve"
if "%~1"=="plugin" if "%~2"=="browser-proxy" set "REQUIRED_CAPABILITY=plugin.browser.serve"
for /f "usebackq delims=" %%I in (`powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%PLUGIN_DIR%\scripts\resolve-openlinker-cli.ps1" -Require "%REQUIRED_CAPABILITY%"`) do set "CLI_PATH=%%I"
if errorlevel 1 exit /b %errorlevel%
if not defined CLI_PATH (
  echo openlinker plugin: resolver returned no CLI path 1>&2
  exit /b 4
)
"%CLI_PATH%" %*
exit /b %errorlevel%
