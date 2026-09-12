@echo off
rem caotun client stop (ASCII only)
rem NOTE: force kill does NOT restore system proxy; run start-client.bat / start-web.bat again and Ctrl+C to restore
taskkill /F /IM caotun.exe
pause
