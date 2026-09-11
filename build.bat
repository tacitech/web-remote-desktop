@echo off
REM Build pc-remote.exe without a console window (-H windowsgui).
REM A plain "go build" works too, but pops up a black window every time it runs.
cd /d "%~dp0"
go build -ldflags="-H windowsgui" -o pc-remote.exe .
if errorlevel 1 ( echo [ERROR] build failed & exit /b 1 )
echo [OK] pc-remote.exe (runs hidden; log goes to pc-remote.log)
