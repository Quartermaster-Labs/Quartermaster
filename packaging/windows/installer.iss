; Inno Setup 6 script for quartermaster.
;
; Per-user install (no UAC) into a writable location, because the app generates
; config\config.yaml at runtime and edits config\quartermaster-generate.yaml.
; exe + start.cmd live in {app}; all config yaml lives in {app}\config; fetched
; backends go under {app}\bin\<component>.
;
; Compile (CI passes these via /D):
;   iscc /DMyAppVersion=v100 /DStagingDir=<abs> /DOutputDir=<abs> installer.iss
;
; # This installer no longer asks anything
;
; It used to carry five custom wizard pages (models folder, which servers,
; download vs existing, compute backend, existing exe paths) that drove
; fetch-backend.ps1 from [Run]. All of that moved into cmd/quartermaster-setup,
; which is what the user actually downloads: a native window that asks the same
; questions against internal/backends -- GPU-detected variants, versioned
; side-by-side installs, staged downloads with rollback, and a PE-imports
; preflight that names a missing GPU runtime instead of failing at first launch.
; The setup program then runs THIS installer with /VERYSILENT.
;
; So what is left here is only what Inno is uniquely good at and nothing else
; provides: the Add/Remove Programs record, the Start Menu group, the
; uninstaller, and an in-place upgrade keyed to a stable AppId.
;
; Nothing in here may prompt, and nothing may reach the network. A silent run
; still executes [Code], so a default-checked option would be silently applied
; to a user who never saw it -- which is exactly the bug the old ServersPage
; defaults would have caused once the setup program started driving this.

#define MyAppName "quartermaster"
#define MyAppExe  "quartermaster-windows-amd64.exe"
#ifndef MyAppVersion
  #define MyAppVersion "0.0.0"
#endif
#ifndef StagingDir
  #define StagingDir "staging"
#endif
#ifndef OutputDir
  #define OutputDir "Output"
#endif

[Setup]
; Stable AppId — keep constant across versions so upgrades replace in place.
AppId={{A7E4C9D2-3B6F-4F1A-9C2E-2D7B8F0A1E55}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher=radu0120
AppPublisherURL=https://github.com/Quartermaster-Labs/quartermaster
DefaultDirName={localappdata}\Programs\{#MyAppName}
; Kept prompting for the manual-run path: the setup program always passes /DIR,
; so this page is only ever seen by someone who ran the inner installer directly.
DisableDirPage=no
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
OutputDir={#OutputDir}
; NOT "quartermaster-setup-...": that name belongs to the outer wizard, which is
; what users download. This is the inner payload it embeds.
OutputBaseFilename=quartermaster-inno-{#MyAppVersion}
; Wizard/setup icon and the Apps & Features entry icon. The path is relative to
; this .iss, i.e. the repo-root favicon.ico that is also embedded in the exe.
SetupIconFile=..\..\favicon.ico
UninstallDisplayIcon={app}\{#MyAppExe}
Compression=lzma2
SolidCompression=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
WizardStyle=modern

[Files]
; Everything staged by CI (binary, examples, start.cmd, packaging\, LICENSE, README).
; Excludes: the staging dir is shared with the release build, which also puts the
; linux/darwin binaries and SHA256SUMS there for the in-app updater to download.
; Those are ~120MB of payload this installer must not carry.
Source: "{#StagingDir}\*"; DestDir: "{app}"; Excludes: "quartermaster-linux-*,quartermaster-darwin-*,SHA256SUMS"; Flags: recursesubdirs createallsubdirs ignoreversion
; Seed the live generate file from the example, but never clobber a user's edits
; on upgrade, and leave it behind on uninstall (it holds their settings).
;
; The setup program edits this file after the installer exits (modelsRoot, the
; backend rows), which is safe precisely because of onlyifdoesntexist: a repair
; run will not overwrite what the wizard wrote.
Source: "{#StagingDir}\config\quartermaster-generate.example.yaml"; DestDir: "{app}\config"; DestName: "quartermaster-generate.yaml"; Flags: onlyifdoesntexist uninsneveruninstall

[Tasks]
; The only remaining task, and the only thing the setup program passes through
; (/TASKS=autostart). Unchecked by default so a manual silent run with no /TASKS
; installs nothing the user did not ask for.
Name: autostart; Description: "Start quartermaster automatically when I log in"; GroupDescription: "Startup:"; Flags: unchecked

[Icons]
Name: "{group}\quartermaster"; Filename: "{app}\start.cmd"; WorkingDir: "{app}"
Name: "{group}\Edit generate config"; Filename: "notepad.exe"; Parameters: """{app}\config\quartermaster-generate.yaml"""; WorkingDir: "{app}"
Name: "{group}\Uninstall quartermaster"; Filename: "{uninstallexe}"
; Logon autostart (per-user Startup folder; console window is the live log).
Name: "{userstartup}\quartermaster"; Filename: "{app}\start.cmd"; WorkingDir: "{app}"; Tasks: autostart

[Run]
; skipifsilent: under the setup program this never fires, because the wizard
; launches the app itself once it has finished installing backends -- starting
; it here would race a server against its own config being written.
Filename: "{app}\start.cmd"; Description: "Launch quartermaster now"; Flags: postinstall shellexec skipifsilent nowait
