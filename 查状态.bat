@echo off
REM Show USB hotspot status
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0hotspot.ps1" -Action status
echo.
pause
