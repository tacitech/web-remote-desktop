@echo off
REM Stop PC Remote (the tunnel is a child process and stops with it).
cd /d "%~dp0"
taskkill /IM pc-remote.exe /F >nul 2>&1
powershell -NoProfile -Command "Get-CimInstance Win32_Process -Filter \"Name='cloudflared.exe'\" | Where-Object { $_.CommandLine -like '*cloudflared-config.yml*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }" >nul 2>&1
echo [PC Remote] stopped.
