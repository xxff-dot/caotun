@echo off
rem caotun Windows build: compile 3 platforms + UPX (auto-download) + collect scripts into dist\
rem Run: double-click, or scripts\windows\build.bat
setlocal
cd /d "%~dp0..\..\src"
if not exist "..\dist" mkdir "..\dist"

echo [1/3] Compiling (strip symbols)...
if exist "..\dist\caotun.exe" del "..\dist\caotun.exe"
if exist "..\dist\caotun_linux" del "..\dist\caotun_linux"
if exist "..\dist\caotun_macos" del "..\dist\caotun_macos"
go build -ldflags "-s -w" -o "..\dist\caotun.exe" . || goto :fail
set GOOS=linux
set GOARCH=amd64
go build -ldflags "-s -w" -o "..\dist\caotun_linux" . || goto :fail
set GOOS=darwin
set GOARCH=arm64
go build -ldflags "-s -w" -o "..\dist\caotun_macos" . || echo darwin build failed (ignored)
set GOOS=
set GOARCH=

echo [2/3] UPX pack (exe/linux)...
where upx >nul 2>&1
if %errorlevel% equ 0 (
  set "UPXEXE=upx"
) else (
  if exist "..\tools\upx\upx.exe" (
    set "UPXEXE=..\tools\upx\upx.exe"
  ) else (
    echo upx not found, auto-downloading...
    if not exist "..\tools\upx" mkdir "..\tools\upx"
    powershell -Command "Invoke-WebRequest -Uri 'https://github.com/upx/upx/releases/download/v5.0.2/upx-5.0.2-win64.zip' -OutFile '..\tools\upx\upx.zip' -UseBasicParsing" || goto :upxfail
    powershell -Command "Expand-Archive -Force '..\tools\upx\upx.zip' '..\tools\upx'" || goto :upxfail
    set "UPXEXE=..\tools\upx\upx.exe"
    goto :upxok
  )
)
:upxok
if not defined UPXEXE goto :upxfail
rem 分文件压缩：单个失败不影响另一个
"%UPXEXE%" -q -f "..\dist\caotun.exe" || echo exe kept uncompressed
"%UPXEXE%" -q -f "..\dist\caotun_linux" || echo linux kept uncompressed
goto :collect
:upxfail
echo UPX auto-download failed, kept uncompressed

:collect
echo [3/3] Collecting scripts into dist...
if not exist "..\dist\shell" mkdir "..\dist\shell"
if not exist "..\dist\windows" mkdir "..\dist\windows"
del "..\dist\*.sh" "..\dist\*.bat" "..\dist\*.conf" 2>nul
copy /y "%~dp0start-web.bat" "..\dist\windows\" >nul
copy /y "%~dp0start-client.bat" "..\dist\windows\" >nul
copy /y "%~dp0start-server.bat" "..\dist\windows\" >nul
copy /y "%~dp0stop-client.bat" "..\dist\windows\" >nul
copy /y "%~dp0stop-server.bat" "..\dist\windows\" >nul
copy /y "%~dp0..\shell\*.sh" "..\dist\shell\" >nul
copy /y "%~dp0..\client.conf" "..\dist\" >nul
copy /y "%~dp0..\server.conf" "..\dist\" >nul

echo.
echo Build OK. Output in dist\
dir /b "..\dist"
pause
exit /b 0

:fail
echo Build FAILED.
pause
exit /b 1
