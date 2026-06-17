@echo off
rem Double-click launcher for llama-quartermaster.
rem Generates config.yaml from quartermaster-generate.yaml (hash-gated), then serves.
rem Edit quartermaster-generate.yaml first: set settings.modelsRoot and settings.serverExe.
setlocal
cd /d "%~dp0"
".\llama-swap-windows-amd64.exe" ^
  -config ".\config.yaml" ^
  -generate ".\quartermaster-generate.yaml" ^
  -listen 0.0.0.0:1250 ^
  -watch-config
echo.
echo llama-quartermaster exited. Press any key to close.
pause >nul
