@echo off
REM ============================================================
REM  PC Remote - one-time setup for remote access
REM
REM  Creates a Cloudflare tunnel for this PC, points a hostname at it,
REM  writes cloudflared-config.yml and registers pc-remote to start
REM  when you log in to Windows.
REM
REM  Usage:   setup.bat <your-domain> [subdomain]
REM  Example: setup.bat example.com pc     ->  https://pc.example.com
REM
REM  Before running: install cloudflared and run  cloudflared tunnel login
REM  (your domain must be on Cloudflare). Run as Administrator so the
REM  start-on-login task can be created.
REM ============================================================
setlocal EnableDelayedExpansion
cd /d "%~dp0"

set "DOMAIN=%~1"
if "%DOMAIN%"=="" (
    echo Usage: setup.bat ^<your-domain^> [subdomain]
    echo Example: setup.bat example.com pc
    exit /b 1
)
set "SUB=%~2"
if "%SUB%"=="" set "SUB=pc"
set "HOSTNAME=%SUB%.%DOMAIN%"
set "TUNNEL=pc-remote-%COMPUTERNAME%"

REM Find cloudflared: next to this script, the usual install dirs, or PATH.
set "CF="
if exist "%~dp0cloudflared.exe" set "CF=%~dp0cloudflared.exe"
if "!CF!"=="" if exist "%ProgramFiles(x86)%\cloudflared\cloudflared.exe" set "CF=%ProgramFiles(x86)%\cloudflared\cloudflared.exe"
if "!CF!"=="" if exist "%ProgramFiles%\cloudflared\cloudflared.exe" set "CF=%ProgramFiles%\cloudflared\cloudflared.exe"
if "!CF!"=="" for %%p in (cloudflared.exe) do if not "%%~$PATH:p"=="" set "CF=%%~$PATH:p"
if "!CF!"=="" (
    echo [ERROR] cloudflared not found.
    echo         Install: https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/
    exit /b 1
)
if not exist "%USERPROFILE%\.cloudflared\cert.pem" (
    echo [ERROR] Not logged in to Cloudflare. Run:  "!CF!" tunnel login
    exit /b 1
)
if not exist "pc-remote.exe" (
    echo [ERROR] pc-remote.exe is missing from this folder. Run build.bat first.
    exit /b 1
)

echo.
echo [1/4] Creating tunnel "%TUNNEL%" (skipped if it already exists)...
"!CF!" tunnel create "%TUNNEL%" >nul 2>&1

REM Read the tunnel ID through a temp file: calling cloudflared (a path with
REM spaces) inside for /f trips over the nested quotes.
"!CF!" tunnel list > "%TEMP%\pcr_tunnels.txt" 2>nul
set "TID="
for /f "tokens=1,2" %%i in ('findstr /C:"%TUNNEL%" "%TEMP%\pcr_tunnels.txt"') do (
    if "%%j"=="%TUNNEL%" set "TID=%%i"
)
del "%TEMP%\pcr_tunnels.txt" >nul 2>&1
if "!TID!"=="" (
    echo [ERROR] Could not read the tunnel ID for "%TUNNEL%".
    exit /b 1
)
echo       Tunnel ID: !TID!

echo [2/4] Routing DNS %HOSTNAME% to the tunnel...
"!CF!" tunnel route dns --overwrite-dns "!TID!" "%HOSTNAME%" 2>nul
if errorlevel 1 echo       (warning: DNS route unchanged - it may already exist)

echo [3/4] Writing cloudflared-config.yml...
> cloudflared-config.yml echo tunnel: !TID!
>> cloudflared-config.yml echo credentials-file: %USERPROFILE%\.cloudflared\!TID!.json
>> cloudflared-config.yml echo.
>> cloudflared-config.yml echo ingress:
>> cloudflared-config.yml echo   - hostname: %HOSTNAME%
>> cloudflared-config.yml echo     service: http://127.0.0.1:7070
>> cloudflared-config.yml echo   - service: http_status:404

echo [4/4] Registering the start-on-login task...
schtasks /Create /TN "PC Remote" /TR "\"%~dp0pc-remote.exe\"" /SC ONLOGON /RL HIGHEST /F >nul 2>&1
if errorlevel 1 (
    echo       (could not create the task - run start.bat by hand, or rerun as Administrator^)
) else (
    echo       Task "PC Remote" registered.
)

REM Generate config.json if missing, so the access code can be shown below.
if not exist config.json (
    start /b "" pc-remote.exe >nul 2>&1
    timeout /t 3 >nul
    taskkill /IM pc-remote.exe /F >nul 2>&1
)
set "TOKEN="
for /f "tokens=2 delims=:," %%t in ('findstr /C:"\"token\"" config.json 2^>nul') do set "TOKEN=%%~t"
set "TOKEN=!TOKEN: =!"
set "TOKEN=!TOKEN:"=!"

echo.
echo ============================================================
echo  Done. Run start.bat to start now (it also starts on login).
echo.
echo  Open on your phone:  https://%HOSTNAME%/
echo  Access code:         !TOKEN!
echo  (the code is the "token" field in config.json)
echo.
echo  SECURITY: consider protecting %HOSTNAME% with Cloudflare Access
echo  (Cloudflare dashboard ^> Zero Trust ^> Access ^> Applications).
echo ============================================================
endlocal
