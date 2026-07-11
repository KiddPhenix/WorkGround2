@echo off
setlocal

set "ROOT=%~dp0"
for %%I in ("%ROOT%.") do set "ROOT=%%~fI"
set "DESKTOP_DIR=%ROOT%\desktop"
set "EXE=%DESKTOP_DIR%\build\bin\WorkGround2.exe"
set "LEGACY_EXE=%DESKTOP_DIR%\build\bin\WorkGround2-desktop.exe"
set "PNPM="

for /f "delims=" %%I in ('where.exe pnpm 2^>nul') do if not defined PNPM set "PNPM=%%I"
if not defined PNPM if defined PNPM_HOME if exist "%PNPM_HOME%\pnpm.cmd" set "PNPM=%PNPM_HOME%\pnpm.cmd"
if not defined PNPM if exist "%APPDATA%\npm\pnpm.cmd" set "PNPM=%APPDATA%\npm\pnpm.cmd"
if not defined PNPM if exist "%LOCALAPPDATA%\pnpm\pnpm.cmd" set "PNPM=%LOCALAPPDATA%\pnpm\pnpm.cmd"
if not defined PNPM if exist "%USERPROFILE%\.cache\codex-runtimes\codex-primary-runtime\dependencies\bin\pnpm.cmd" set "PNPM=%USERPROFILE%\.cache\codex-runtimes\codex-primary-runtime\dependencies\bin\pnpm.cmd"
if not defined PNPM if exist "%ProgramFiles%\nodejs\pnpm.cmd" set "PNPM=%ProgramFiles%\nodejs\pnpm.cmd"
if not defined PNPM (
  echo [WorkGround2] pnpm not found. Install it with:
  echo   npm.cmd install -g pnpm
  exit /b 1
)
for %%I in ("%PNPM%") do set "PATH=%%~dpI;%PATH%"
echo [WorkGround2] using pnpm: "%PNPM%"

pushd "%DESKTOP_DIR%" || exit /b 1

set "WAILS=wails"
where.exe wails >nul 2>nul
if errorlevel 1 (
  if exist "%USERPROFILE%\go\bin\wails.exe" (
    set "WAILS=%USERPROFILE%\go\bin\wails.exe"
  ) else (
    echo [WorkGround2] wails not found. Install it with:
    echo   go install github.com/wailsapp/wails/v2/cmd/wails@latest
    popd
    exit /b 1
  )
)

echo [WorkGround2] building desktop app...
"%WAILS%" build -clean
set "BUILD_EXIT=%ERRORLEVEL%"
popd
if not "%BUILD_EXIT%"=="0" exit /b %BUILD_EXIT%

if not exist "%EXE%" (
  echo [WorkGround2] build succeeded but output was not found:
  echo   "%EXE%"
  exit /b 1
)

echo [WorkGround2] build complete: "%EXE%"
exit /b 0
