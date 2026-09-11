@echo off
REM ============================================================
REM  PC Remote - start
REM  Only the exe is needed: it supervises the Cloudflare tunnel itself
REM  (and restarts it if it dies). Run setup.bat once before this.
REM ============================================================
cd /d "%~dp0"

REM Stop a previous instance so port 7070 and the tunnel are free.
taskkill /IM pc-remote.exe /F >nul 2>&1
powershell -NoProfile -Command "Get-CimInstance Win32_Process -Filter \"Name='cloudflared.exe'\" | Where-Object { $_.CommandLine -like '*cloudflared-config.yml*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }" >nul 2>&1
ping -n 2 127.0.0.1 >nul

echo [PC Remote] starting...
start "" /b pc-remote.exe >> pc-remote.log 2>&1
ping -n 4 127.0.0.1 >nul
echo [PC Remote] running. Logs: pc-remote.log / tunnel.log
