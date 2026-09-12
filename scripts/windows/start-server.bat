@echo off
rem caotun server starter for Windows (ASCII only). Config: ..\server.conf
setlocal
set "CONF=%~dp0..\server.conf"
set "HOST=0.0.0.0"
set "PORT=443"
set "WS_PORT=8443"
set "DOMAIN="
set "CERT="
set "KEY="
set "MAX_GB=20"
set "QUOTA_DAYS=30"
set "MAX_CONNS=100"
set "ROTATE="
set "AUTH="
if exist "%CONF%" for %%k in (HOST PORT WS_PORT DOMAIN CERT KEY MAX_GB QUOTA_DAYS MAX_CONNS ROTATE AUTH) do (
  for /f "tokens=1,* delims==" %%a in ('findstr /b /c:"%%k=" "%CONF%"') do set "%%a=%%b"
)

set "AUTHDIR=%USERPROFILE%\.caotun"
if not exist "%AUTHDIR%" mkdir "%AUTHDIR%"
if defined AUTH <nul set /p ="%AUTH%">"%AUTHDIR%\auth"

set "ARGS=server -host %HOST% -port %PORT% -ws-port %WS_PORT% -max-gb %MAX_GB% -quota-days %QUOTA_DAYS% -max-conns %MAX_CONNS%"
if "%ROTATE%"=="1" set "ARGS=%ARGS% -rotate-pass"
if defined CERT (
  if defined KEY set "ARGS=%ARGS% -cert %CERT% -key %KEY%"
) else (
  if defined DOMAIN set "ARGS=%ARGS% -domain %DOMAIN%"
)

set "BINDIR=%~dp0..\..\dist"
if not exist "%BINDIR%\caotun.exe" set "BINDIR=%~dp0.."
if not exist "%BINDIR%\caotun.exe" set "BINDIR=%~dp0..\.."
if not exist "%BINDIR%\caotun.exe" echo [ERROR] caotun.exe not found, run scripts\windows\build.bat first & pause & exit /b 1

taskkill /F /IM caotun.exe >nul 2>&1
echo starting: caotun.exe %ARGS%
"%BINDIR%\caotun.exe" %ARGS%
pause
