@echo off
rem Double-click launcher for llama-quartermaster.
rem Generates config.yaml from quartermaster-generate.yaml (hash-gated), then serves.
rem Edit quartermaster-generate.yaml first: set settings.modelsRoot and settings.serverExe.
rem Dashboard on localhost:1250 (this machine only), Playground app LAN-exposed on :8081 (open http://localhost:8081/ui/).
setlocal
cd /d "%~dp0"
".\llama-quartermaster-windows-amd64.exe" ^
  -config ".\config.yaml" ^
  -generate ".\quartermaster-generate.yaml" ^
  -listen 127.0.0.1:1250 ^
  -playground-port 0.0.0.0:8081 ^
  -watch-config
echo.
echo llama-quartermaster exited. Press any key to close.
pause >nul
