@echo off
REM Stop USB hotspot
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0hotspot.ps1" -Action stop
echo.
pause
