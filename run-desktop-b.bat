@echo off
setlocal

cd /d "%~dp0"

set "INSTANCE_ROOT=%TEMP%\WorkGround2-test-b"
set "WorkGround2_DEV=1"
set "WorkGround2_HOME=%INSTANCE_ROOT%\home"
set "WorkGround2_STATE_HOME=%INSTANCE_ROOT%\state"
set "WorkGround2_CACHE_HOME=%INSTANCE_ROOT%\cache"
set "APPDATA=%INSTANCE_ROOT%\appdata"
set "LOCALAPPDATA=%INSTANCE_ROOT%\localappdata"
set "TEMP=%INSTANCE_ROOT%\temp"
set "TMP=%INSTANCE_ROOT%\temp"
set "APP_EXE=%~dp0desktop\build\bin\WorkGround2.exe"

if not exist "%TEMP%" mkdir "%TEMP%"

if not exist "%APP_EXE%" (
    echo WorkGround2.exe not found:
    echo %APP_EXE%
    echo Run "wails build" in the desktop directory first.
    pause
    exit /b 1
)

start "WorkGround2 B" "%APP_EXE%"
exit /b 0
