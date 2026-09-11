@echo off
setlocal
set "PLUGIN_DIR=%~dp0.."
set "HOST_PATH="
set "REQUIRED_CAPABILITY=plugin.serve"
if "%~1"=="plugin" if "%~2"=="browser-serve" set "REQUIRED_CAPABILITY=plugin.browser.serve"
if "%~1"=="plugin" if "%~2"=="browser-proxy" set "REQUIRED_CAPABILITY=plugin.browser.serve"
for /f "usebackq delims=" %%I in (`node "%PLUGIN_DIR%\scripts\resolve-plugin-host.mjs" --require "%REQUIRED_CAPABILITY%"`) do set "HOST_PATH=%%I"
if errorlevel 1 exit /b %errorlevel%
if not defined HOST_PATH (
  echo openlinker plugin: resolver returned no Plugin host path 1>&2
  exit /b 4
)
"%HOST_PATH%" %*
exit /b %errorlevel%
