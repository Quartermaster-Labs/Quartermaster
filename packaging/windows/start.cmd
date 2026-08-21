@echo off
rem Double-click launcher for quartermaster.
rem Generates config.yaml from quartermaster-generate.yaml (hash-gated), then serves.
rem Edit config\quartermaster-generate.yaml first: set settings.modelsRoot and settings.serverExe.
rem API (/v1/*) exposed on the LAN/tailnet at :1250; the dashboard and admin
rem endpoints on that same port answer to this machine only (see -admin-allow).
rem Playground app LAN-exposed on :8081 (open http://localhost:8081/ui/).
setlocal
cd /d "%~dp0"
rem -app opens the dashboard in a desktop window and implies -tray. Called as
rem `start.cmd background` it drops to -tray only: that is how the "start with
rem Windows" shortcut runs it, because a window appearing unasked at every login
rem is not what ticking an autostart box means. -app also hands off to an
rem already-running instance, so a second double-click raises that window
rem instead of failing to bind the port.
set "QM_UI=-app"
if /i "%~1"=="background" set "QM_UI=-tray"

rem `start ""` launches the exe detached; the exe is built -H=windowsgui so it
rem has no console. This cmd window then exits immediately (brief flash),
rem leaving only the window and/or the tray icon.
start "" ".\quartermaster-windows-amd64.exe" ^
  -config ".\config\config.yaml" ^
  -generate ".\config\quartermaster-generate.yaml" ^
  -listen 0.0.0.0:1250 ^
  -playground-port 0.0.0.0:8081 ^
  -watch-config ^
  %QM_UI%
