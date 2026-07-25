@echo off
rem Double-click launcher for quartermaster.
rem Generates config.yaml from quartermaster-generate.yaml (hash-gated), then serves.
rem Edit config\quartermaster-generate.yaml first: set settings.modelsRoot and settings.serverExe.
rem API (/v1/*) exposed on the LAN/tailnet at :1250; the dashboard and admin
rem endpoints on that same port answer to this machine only (see -admin-allow).
rem Playground app LAN-exposed on :8081 (open http://localhost:8081/ui/).
setlocal
cd /d "%~dp0"
rem `start ""` launches the exe detached; the exe is built -H=windowsgui so it
rem has no console, and -tray gives it a system-tray icon. This cmd window then
rem exits immediately (brief flash), leaving only the tray icon.
start "" ".\quartermaster-windows-amd64.exe" ^
  -config ".\config\config.yaml" ^
  -generate ".\config\quartermaster-generate.yaml" ^
  -listen 0.0.0.0:1250 ^
  -playground-port 0.0.0.0:8081 ^
  -watch-config ^
  -tray
