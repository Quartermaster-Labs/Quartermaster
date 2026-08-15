; Inno Setup 6 script for quartermaster.
;
; Per-user install (no UAC) into a writable location, because the app generates
; config\config.yaml at runtime and edits config\quartermaster-generate.yaml.
; exe + start.cmd live in {app}; all config yaml lives in {app}\config; start.cmd
; resolves them via %~dp0; fetched backends go under {app}\bin\<component>.
;
; Compile (CI passes these via /D):
;   iscc /DMyAppVersion=v100 /DStagingDir=<abs> /DOutputDir=<abs> installer.iss
;
; Wizard offers to download llama-server / sd-server / tts-server for a chosen
; backend (vulkan/cuda/cpu) and an optional logon-autostart shortcut.

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
; Always prompt for the install location (default 'auto' hides it on upgrade).
DisableDirPage=no
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
OutputDir={#OutputDir}
OutputBaseFilename=quartermaster-setup-{#MyAppVersion}
; Wizard/setup icon and the Apps & Features entry icon. The path is relative to
; this .iss, i.e. the repo-root favicon.ico that is also embedded in the exe.
SetupIconFile=..\..\favicon.ico
UninstallDisplayIcon={app}\{#MyAppExe}
Compression=lzma2
SolidCompression=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
WizardStyle=modern
UninstallDisplayIcon={app}\{#MyAppExe}

[Files]
; Everything staged by CI (binary, examples, start.cmd, packaging\, LICENSE, README).
Source: "{#StagingDir}\*"; DestDir: "{app}"; Flags: recursesubdirs createallsubdirs ignoreversion
; Seed the live generate file from the example, but never clobber a user's edits
; on upgrade, and leave it behind on uninstall (it holds their settings).
Source: "{#StagingDir}\config\quartermaster-generate.example.yaml"; DestDir: "{app}\config"; DestName: "quartermaster-generate.yaml"; Flags: onlyifdoesntexist uninsneveruninstall

[Tasks]
Name: autostart; Description: "Start quartermaster automatically when I log in"; GroupDescription: "Startup:"; Flags: unchecked
; A Task, not a ServersPage checkbox: yt-dlp is not an inference backend, has no
; compute-backend or existing-install variant, and must be fetchable even when
; the user configures no servers at all. ~17MB standalone exe (bundles Python).
Name: ytdlp; Description: "yt-dlp - lets chat models read YouTube video transcripts"; GroupDescription: "Optional helpers:"

[Icons]
Name: "{group}\quartermaster"; Filename: "{app}\start.cmd"; WorkingDir: "{app}"
Name: "{group}\Edit generate config"; Filename: "notepad.exe"; Parameters: """{app}\config\quartermaster-generate.yaml"""; WorkingDir: "{app}"
Name: "{group}\Uninstall quartermaster"; Filename: "{uninstallexe}"
; Logon autostart (per-user Startup folder; console window is the live log).
Name: "{userstartup}\quartermaster"; Filename: "{app}\start.cmd"; WorkingDir: "{app}"; Tasks: autostart

[Run]
; Set the models folder in the generate yaml (independent of server setup).
Filename: "powershell.exe"; \
  Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\packaging\windows\fetch-backend.ps1"" -ModelsRoot ""{code:GetModelsRoot}"" -AppDir ""{app}"" -NoPause"; \
  StatusMsg: "Setting models folder..."; \
  Flags: runhidden waituntilterminated; \
  Check: HasModelsRoot
; Download chosen backends from GitHub, then wire them into the generate yaml.
Filename: "powershell.exe"; \
  Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\packaging\windows\fetch-backend.ps1"" -Backend {code:GetBackend} -Components ""{code:GetComponents}"" -AppDir ""{app}"" -NoPause"; \
  StatusMsg: "Downloading inference backends from GitHub..."; \
  Flags: runhidden waituntilterminated; \
  Check: IsDownloadMode
; Point the generate yaml at the user's existing installs (no download).
Filename: "powershell.exe"; \
  Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\packaging\windows\fetch-backend.ps1"" -LlamaExe ""{code:GetLlamaExe}"" -SdExe ""{code:GetSdExe}"" -TtsExe ""{code:GetTtsExe}"" -AppDir ""{app}"" -NoPause"; \
  StatusMsg: "Configuring existing inference backends..."; \
  Flags: runhidden waituntilterminated; \
  Check: IsExistingMode
; Chat-title model (~79 MB). No longer shipped in the binary; prefetched here so
; the first chat has titles immediately. The server fetches it lazily otherwise.
Filename: "powershell.exe"; \
  Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\packaging\windows\fetch-backend.ps1"" -TitleModel -AppDir ""{app}"" -NoPause"; \
  StatusMsg: "Downloading chat title model..."; \
  Flags: runhidden waituntilterminated
; Optional helper, independent of the backend source mode above.
Filename: "powershell.exe"; \
  Parameters: "-NoProfile -ExecutionPolicy Bypass -File ""{app}\packaging\windows\fetch-backend.ps1"" -Components ""yt-dlp"" -AppDir ""{app}"" -NoPause"; \
  StatusMsg: "Downloading yt-dlp..."; \
  Flags: runhidden waituntilterminated; \
  Tasks: ytdlp
Filename: "{app}\start.cmd"; Description: "Launch quartermaster now"; Flags: postinstall shellexec skipifsilent nowait

[Code]
var
  ModelsPage:   TInputDirWizardPage;     { GGUF models folder }
  ServersPage:  TInputOptionWizardPage;  { which servers to configure (checkboxes) }
  SourcePage:   TInputOptionWizardPage;  { download vs point at existing (radio) }
  BackendPage:  TInputOptionWizardPage;  { which backend to download (radio) }
  ExistingPage: TInputFileWizardPage;    { paths to existing exes }

procedure InitializeWizard;
begin
  ModelsPage := CreateInputDirPage(wpSelectTasks,
    'Models folder',
    'Where are your GGUF model files?',
    'Quartermaster scans this folder recursively for *.gguf. Leave it blank to' + #13#10 +
    'set it later from the dashboard.',
    False, '');
  ModelsPage.Add('');

  ServersPage := CreateInputOptionPage(ModelsPage.ID,
    'Inference backends',
    'Choose the model servers quartermaster drives.',
    'These are separate projects (MIT/Apache). Uncheck both to configure your' + #13#10 +
    'own paths later.',
    False, False);  { ExclusionList=False -> checkboxes }
  ServersPage.Add('llama-server (text models)  - ggml-org/llama.cpp');
  ServersPage.Add('sd-server (image models)    - leejet/stable-diffusion.cpp');
  ServersPage.Add('tts-server (speech models)  - ServeurpersoCom/qwentts.cpp');
  ServersPage.Values[0] := True;
  ServersPage.Values[1] := True;
  { tts off by default: niche, and qwentts.cpp has no prebuilt release to
    download yet, so it is realistically an existing-install (pick .exe) choice. }
  ServersPage.Values[2] := False;

  SourcePage := CreateInputOptionPage(ServersPage.ID,
    'Backend source',
    'Download prebuilt servers, or point at installs you already have.',
    '',
    True, False);  { ExclusionList=True -> radio }
  SourcePage.Add('Download the latest prebuilt build from GitHub');
  SourcePage.Add('Use existing installs (I''ll pick the .exe paths)');
  SourcePage.SelectedValueIndex := 0;

  BackendPage := CreateInputOptionPage(SourcePage.ID,
    'Compute backend',
    'Pick the build to download for the servers selected above.',
    'Vulkan runs on any modern GPU without the CUDA toolkit (safe default).' + #13#10 +
    'CUDA gives best NVIDIA performance but is a larger download.' + #13#10 +
    'CPU has no GPU acceleration.',
    True, False);
  BackendPage.Add('Vulkan (recommended)');
  BackendPage.Add('CUDA (NVIDIA)');
  BackendPage.Add('CPU only');
  BackendPage.SelectedValueIndex := 0;

  ExistingPage := CreateInputFilePage(SourcePage.ID,
    'Existing installs',
    'Point at the server executables you already have.',
    'Leave a field blank to skip that server.');
  ExistingPage.Add('llama-server executable:', 'Executable (*.exe)|*.exe', '.exe');
  ExistingPage.Add('sd-server executable:',    'Executable (*.exe)|*.exe', '.exe');
  ExistingPage.Add('tts-server executable:',   'Executable (*.exe)|*.exe', '.exe');
end;

{ True if the user wants at least one server configured. }
function WantServers: Boolean;
begin
  Result := ServersPage.Values[0] or ServersPage.Values[1] or ServersPage.Values[2];
end;

function IsDownloadMode: Boolean;
begin
  Result := WantServers and (SourcePage.SelectedValueIndex = 0);
end;

function IsExistingMode: Boolean;
begin
  Result := WantServers and (SourcePage.SelectedValueIndex = 1);
end;

{ Skip pages that don't apply to the current choices. }
function ShouldSkipPage(PageID: Integer): Boolean;
begin
  if PageID = SourcePage.ID then
    Result := not WantServers
  else if PageID = BackendPage.ID then
    Result := not IsDownloadMode
  else if PageID = ExistingPage.ID then
    Result := not IsExistingMode
  else
    Result := False;
end;

function GetBackend(Param: String): String;
begin
  case BackendPage.SelectedValueIndex of
    1: Result := 'cuda';
    2: Result := 'cpu';
  else
    Result := 'vulkan';
  end;
end;

function GetComponents(Param: String): String;
var
  parts: String;
begin
  parts := '';
  if ServersPage.Values[0] then parts := 'llama-server';
  if ServersPage.Values[1] then begin
    if parts <> '' then parts := parts + ',';
    parts := parts + 'sd-server';
  end;
  if ServersPage.Values[2] then begin
    if parts <> '' then parts := parts + ',';
    parts := parts + 'tts-server';
  end;
  Result := parts;
end;

{ Existing-mode exe paths, only for the servers the user ticked. }
function GetLlamaExe(Param: String): String;
begin
  if ServersPage.Values[0] then Result := ExistingPage.Values[0] else Result := '';
end;

function GetSdExe(Param: String): String;
begin
  if ServersPage.Values[1] then Result := ExistingPage.Values[1] else Result := '';
end;

function GetTtsExe(Param: String): String;
begin
  if ServersPage.Values[2] then Result := ExistingPage.Values[2] else Result := '';
end;

function GetModelsRoot(Param: String): String;
begin
  Result := ModelsPage.Values[0];
end;

function HasModelsRoot: Boolean;
begin
  Result := ModelsPage.Values[0] <> '';
end;
