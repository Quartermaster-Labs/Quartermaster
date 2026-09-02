; Inno Setup 6 script for quartermaster.
;
; Per-user install (no UAC) into a writable location, because the app generates
; config\config.yaml at runtime and edits config\quartermaster-generate.yaml.
; Quartermaster.exe lives in {app}; all config yaml lives in {app}\config; fetched
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
; provides: the Add/Remove Programs record, the Start Menu group and desktop
; shortcut, the uninstaller, and an in-place upgrade keyed to a stable AppId.
;
; Nothing in here may prompt, and nothing may reach the network. A silent run
; still executes [Code], so a default-checked option would be silently applied
; to a user who never saw it -- which is exactly the bug the old ServersPage
; defaults would have caused once the setup program started driving this.

; MyAppName is display text (Add/Remove Programs, the Start Menu group).
; MyAppDir is the folder name, kept lowercase and UNCHANGED: it is the default
; install path, and capitalising it would move where a fresh install lands while
; upgrades (keyed to AppId) stayed put.
#define MyAppName "Quartermaster"
#define MyAppDir  "quartermaster"
; The installed binary. NOT the release-asset name -- CI still publishes
; quartermaster-windows-amd64.exe, because internal/update matches that asset
; exactly and an install from before the rename must keep finding it. The
; updater swaps whatever path it is running from, so the two names are free to
; differ; [Files] below excludes the asset-named copy from {app}.
#define MyAppExe  "Quartermaster.exe"
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
AppPublisher=QuartermasterLabs
AppPublisherURL=https://github.com/Quartermaster-Labs/Quartermaster
DefaultDirName={localappdata}\Programs\{#MyAppDir}
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
; this .iss. It reaches into cmd/quartermaster because that is where the icon has to
; live: Go links a .syso only from the main package directory and //go:embed cannot
; reach above it, so the exe's own icon source sits there and this points at the
; same file rather than keeping a second copy in sync.
SetupIconFile=..\..\cmd\quartermaster\favicon.ico
UninstallDisplayIcon={app}\{#MyAppExe}
Compression=lzma2
SolidCompression=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
WizardStyle=modern

[Files]
; Everything staged by CI (binary, examples, packaging\, LICENSE, README).
; Excludes: the staging dir is shared with the release build, which also puts the
; linux/darwin binaries and SHA256SUMS there for the in-app updater to download.
; Those are ~120MB of payload this installer must not carry.
Source: "{#StagingDir}\*"; DestDir: "{app}"; Excludes: "quartermaster-linux-*,quartermaster-darwin-*,quartermaster-windows-amd64.exe,SHA256SUMS"; Flags: recursesubdirs createallsubdirs ignoreversion
; Seed the live generate file from the example, but never clobber a user's edits
; on upgrade, and leave it behind on uninstall (it holds their settings).
;
; The setup program edits this file after the installer exits (modelsRoot, the
; backend rows), which is safe precisely because of onlyifdoesntexist: a repair
; run will not overwrite what the wizard wrote.
Source: "{#StagingDir}\config\quartermaster-generate.example.yaml"; DestDir: "{app}\config"; DestName: "quartermaster-generate.yaml"; Flags: onlyifdoesntexist uninsneveruninstall

[InstallDelete]
; The binary was renamed quartermaster-windows-amd64.exe -> Quartermaster.exe.
; Drop the old name on upgrade, or {app} keeps two copies and anything still
; pointing at the stale one (a hand-made shortcut, a Run-key entry the app wrote
; before the rename) launches a binary that no longer gets updated.
Type: files; Name: "{app}\quartermaster-windows-amd64.exe"

; start.cmd is gone: the exe carries its own launch flags (see bundle.go), so the
; launcher script had nothing left to do. Drop the copy an older install left in
; {app}, or a hand-made shortcut still pointing at it keeps working and keeps the
; script alive by habit.
Type: files; Name: "{app}\start.cmd"

; Unticking a shortcut has to REMOVE it, not just skip creating it. Inno leaves
; icons from a previous install alone when their task is deselected, so an
; upgrade (or a second wizard run over the same install) would otherwise be
; unable to take a shortcut away once it had been granted.
Type: filesandordirs; Name: "{group}"; Tasks: not startmenu
Type: files; Name: "{autodesktop}\{#MyAppName}.lnk"; Tasks: not desktopicon
; The Startup folder needs the same treatment, and used to be the one place that
; did not get it: an old link there pointed at start.cmd, and an upgrade with the
; box unticked would have left it aimed at a deleted script.
Type: files; Name: "{userstartup}\{#MyAppName}.lnk"; Tasks: not autostart

[Tasks]
; What the setup program passes through as a /TASKS= list. ALL of them are
; unchecked by default, including the Start Menu group that used to be
; unconditional: a silent run still executes this script, so a default-checked
; task would be applied to someone who never saw the checkbox. The wizard ticks
; Start Menu for the user, which is where that default belongs -- on the screen
; that shows it.
Name: startmenu; Description: "Add a Start Menu entry"; GroupDescription: "Shortcuts:"; Flags: unchecked
Name: desktopicon; Description: "Create a desktop shortcut"; GroupDescription: "Shortcuts:"; Flags: unchecked
Name: autostart; Description: "Start Quartermaster automatically when I log in"; GroupDescription: "Startup:"; Flags: unchecked

[Icons]
; Straight to the exe: it supplies its own flags when it finds config\ next to
; it (see bundle.go), so there is no launcher script to wear the wrong icon or
; to flash a console window on the way to the dashboard.
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExe}"; WorkingDir: "{app}"; Tasks: startmenu
Name: "{group}\Edit generate config"; Filename: "notepad.exe"; Parameters: """{app}\config\quartermaster-generate.yaml"""; WorkingDir: "{app}"; Tasks: startmenu
Name: "{group}\Uninstall {#MyAppName}"; Filename: "{uninstallexe}"; Tasks: startmenu
; Desktop shortcut. {autodesktop} resolves to the per-user desktop under
; PrivilegesRequired=lowest, so this never writes to the all-users desktop.
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExe}"; WorkingDir: "{app}"; Tasks: desktopicon
; Logon autostart (per-user Startup folder). -tray keeps the login launch
; windowless: a window appearing unasked at every logon is not what ticking an
; autostart box means.
Name: "{userstartup}\{#MyAppName}"; Filename: "{app}\{#MyAppExe}"; Parameters: "-tray"; WorkingDir: "{app}"; Tasks: autostart

[Run]
; skipifsilent: under the setup program this never fires, because the wizard
; launches the app itself once it has finished installing backends -- starting
; it here would race a server against its own config being written.
Filename: "{app}\{#MyAppExe}"; Description: "Launch {#MyAppName} now"; Flags: postinstall skipifsilent nowait
