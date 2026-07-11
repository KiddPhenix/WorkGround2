@echo off
setlocal EnableExtensions
set "ROOT=%~dp0"
set "DESKTOP_DIR=%ROOT%desktop"
set "QA_EXE=%DESKTOP_DIR%\build\bin\WorkGround2-iris-qa.exe"

echo [1/3] Stopping old WorkGround2-iris-qa.exe...
powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$target=[IO.Path]::GetFullPath($env:QA_EXE); Get-CimInstance Win32_Process ^| Where-Object { $_.ExecutablePath -and [IO.Path]::GetFullPath($_.ExecutablePath) -eq $target } ^| ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction Stop }"
if errorlevel 1 (
  echo [ERROR] Failed to stop the old QA process.
  exit /b 1
)

where wails >nul 2>nul
if errorlevel 1 (
  echo [ERROR] Wails CLI was not found in PATH.
  exit /b 1
)

echo [2/3] Rebuilding WorkGround2-iris-qa.exe...
pushd "%DESKTOP_DIR%"
wails build -o WorkGround2-iris-qa.exe -m -nosyncgomod
set "BUILD_EXIT=%ERRORLEVEL%"
popd
if not "%BUILD_EXIT%"=="0" (
  echo [ERROR] Wails build failed with exit code %BUILD_EXIT%.
  exit /b %BUILD_EXIT%
)

if not exist "%QA_EXE%" (
  echo [ERROR] Build succeeded but output was not found: %QA_EXE%
  exit /b 1
)

echo [3/3] Starting WorkGround2-iris-qa.exe...
start "" "%QA_EXE%"
if errorlevel 1 (
  echo [ERROR] Failed to start the QA executable.
  exit /b 1
)

echo [DONE] QA executable was rebuilt and started.
exit /b 0
