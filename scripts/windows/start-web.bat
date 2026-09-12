@echo off
rem caotun web panel launcher (ASCII only - encoding proof)
rem Stop: click the minimized panel window in taskbar, press Ctrl+C (restores system proxy)
rem Config: ..\client.conf (WEB_PORT, default 21877)
setlocal
set "WEB_PORT=21877"
if exist "%~dp0..\client.conf" for /f "tokens=1,* delims==" %%a in ('findstr /b /c:"WEB_PORT=" "%~dp0..\client.conf"') do set "WEB_PORT=%%b"

set "BINDIR=%~dp0..\..\dist"
if not exist "%BINDIR%\caotun.exe" set "BINDIR=%~dp0.."
if not exist "%BINDIR%\caotun.exe" set "BINDIR=%~dp0..\.."
if not exist "%BINDIR%\caotun.exe" echo [ERROR] caotun.exe not found, run scripts\windows\build.bat first & pause & exit /b 1

taskkill /F /IM caotun.exe >nul 2>&1
start "caotun-panel" /min "%BINDIR%\caotun.exe" web -web-port %WEB_PORT%
ping -n 3 127.0.0.1 >nul
start http://127.0.0.1:%WEB_PORT%
