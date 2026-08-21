@echo off
rem Back-compat shim. The exe now carries its own launch flags -- when it finds
rem config\quartermaster-generate.yaml next to itself it fills in -config, -generate,
rem both listeners, -watch-config and -app on its own (see bundle.go). Double-
rem click the exe. This file is still shipped only so shortcuts and Startup
rem entries left by older installs keep working.
rem
rem   start.cmd              -> the app window (same as double-clicking the exe)
rem   start.cmd background   -> tray only, no window (the old autostart form)
setlocal
cd /d "%~dp0"
start "" ".\Quartermaster.exe" %*
