@echo off
REM Start USB hotspot (auto-handles Clash TUN to avoid disconnection)
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0hotspot.ps1" -Action start
echo.
pause
