@echo off
rem caotun client starter (ASCII only). Config: ..\client.conf
rem Stop: press Ctrl+C in this window (restores system proxy)
setlocal
set "CONF=%~dp0..\client.conf"
set "DIRECT_ADDR="
set "AUTH_PASSWORD="
set "LOCAL_PORT=21878"
set "HOST="
set "SYS_PROXY=off"
set "WS="
set "INSECURE="
set "PAC_PORT="
set "DNS="
if exist "%CONF%" for %%k in (DIRECT_ADDR AUTH_PASSWORD LOCAL_PORT HOST SYS_PROXY WS INSECURE PAC_PORT DNS) do (
  for /f "tokens=1,* delims==" %%a in ('findstr /b /c:"%%k=" "%CONF%"') do set "%%a=%%b"
)
if not defined DIRECT_ADDR echo [ERROR] client.conf missing DIRECT_ADDR & pause & exit /b 1
if not defined AUTH_PASSWORD echo [ERROR] client.conf missing AUTH_PASSWORD & pause & exit /b 1

set "BINDIR=%~dp0..\..\dist"
if not exist "%BINDIR%\caotun.exe" set "BINDIR=%~dp0.."
if not exist "%BINDIR%\caotun.exe" set "BINDIR=%~dp0..\.."
if not exist "%BINDIR%\caotun.exe" echo [ERROR] caotun.exe not found, run scripts\windows\build.bat first & pause & exit /b 1

if not exist "%USERPROFILE%\.caotun" mkdir "%USERPROFILE%\.caotun"
<nul set /p ="%AUTH_PASSWORD%">"%USERPROFILE%\.caotun\auth"

set "ARGS=client -server-addr %DIRECT_ADDR% -lport %LOCAL_PORT% -sysproxy %SYS_PROXY%"
if defined HOST set "ARGS=%ARGS% -lhost %HOST%"
if "%WS%"=="1" set "ARGS=%ARGS% -ws"
if "%INSECURE%"=="1" set "ARGS=%ARGS% -insecure"
if defined PAC_PORT set "ARGS=%ARGS% -pac-port %PAC_PORT%"
if defined DNS set "ARGS=%ARGS% -dns %DNS%"

taskkill /F /IM caotun.exe >nul 2>&1
echo starting: caotun.exe %ARGS%
"%BINDIR%\caotun.exe" %ARGS%
pause
