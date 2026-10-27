; SerwisDiag (wersja Go) - skrypt instalatora dla Inno Setup 7 (https://jrsoftware.org). Plik w UTF-8 z BOM.
; Budowanie: powershell -ExecutionPolicy Bypass -File tools\Build-Release.ps1 - buduje pliki programu w folderze
; tymczasowym, podaje wersję, folder z plikami i plik licencji, a gotowy SerwisDiag-Go-<wersja>-instalator.exe
; zapisuje w dist\ razem z sumą SHA-256 (.sha256 - sprawdza ją aktualizacja z programu).

#ifndef AppVersion
  #error Podaj wersję: /DAppVersion=x.y.z (robi to tools\Build-Release.ps1)
#endif
#ifndef SourceDir
  #error Podaj folder z plikami programu: /DSourceDir=... (robi to tools\Build-Release.ps1)
#endif
#ifndef LicensePath
  #define LicensePath SourceDir + "\LICENSE"
#endif
#ifndef IconPath
  #define IconPath "..\..\winres\icon.ico"
#endif
#ifndef OutDir
  #define OutDir "..\..\dist"
#endif
; AppId nie może się zmieniać między wersjami - po nim instalator rozpoznaje poprzednią instalację i ją aktualizuje.
; Wersja Go ma własny identyfikator i folder: instaluje się obok wersji PowerShell, nie zamiast niej.
#ifndef AppIdGuid
  #define AppIdGuid "DF7B112B-A4AB-46D7-B8BB-783BA2BAD4C9"
#endif
#ifndef AppTitle
  #define AppTitle "SerwisDiag Go"
#endif
; /DRegistration=1 (Build-Release.ps1, gdy program ma adres formularza rejestracji): pole „Zarejestruj program” na końcu
#ifndef Registration
  #define Registration 0
#endif

[Setup]
AppId={{{#AppIdGuid}}
AppName={#AppTitle}
AppVersion={#AppVersion}
AppPublisher=Kacper Suliński - INDUSIP.PL
AppCopyright=© 2026 Kacper Suliński - INDUSIP.PL
VersionInfoVersion={#AppVersion}
VersionInfoDescription=Instalator programu SerwisDiag (wersja Go)
DefaultDirName={autopf}\{#AppTitle}
DisableProgramGroupPage=yes
; Domyślnie dla wszystkich użytkowników, w Program Files (tam plików programu nie podmieni proces bez uprawnień
; administratora). „Tylko dla mnie” (okno wyboru albo /CURRENTUSER) nie wymaga UAC.
PrivilegesRequired=admin
PrivilegesRequiredOverridesAllowed=dialog commandline
; program w Go: Windows 10 i 11, 64-bitowy
MinVersion=10.0
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
LicenseFile={#LicensePath}
SetupIconFile={#IconPath}
UninstallDisplayIcon={app}\serwisdiag.exe
UninstallDisplayName={#AppTitle}
OutputDir={#OutDir}
OutputBaseFilename=SerwisDiag-Go-{#AppVersion}-instalator
WizardStyle=modern
Compression=lzma2/max
SolidCompression=yes
; monitor przy zegarze innego użytkownika albo okno programu trzymają plik programu - Menedżer ponownego
; uruchamiania zamyka je przed podmianą plików (monitor tego użytkownika kończy wcześniej -StopMonitor)
CloseApplications=yes
RestartApplications=no

[Languages]
Name: "polish"; MessagesFile: "compiler:Languages\Polish.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"
Name: "monitor"; Description: "Monitoruj system w tle (ikona przy zegarze, powiadomienia o nowych problemach)"; GroupDescription: "Monitorowanie:"

[Dirs]
; ustawienia serwisu (config\settings.json) są wspólne dla użytkowników komputera, a program działa bez uprawnień
; administratora - w Program Files użytkownicy muszą móc je zapisać
Name: "{app}\config"; Permissions: users-modify; Check: IsAdminInstallMode

[Files]
Source: "{#SourceDir}\serwisdiag.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\serwisdiag-konsola.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\LICENSE"; DestDir: "{app}"; Flags: ignoreversion

[INI]
; znacznik kopii zainstalowanej: program zapisuje wtedy raporty w Dokumenty\SerwisDiag, a nie obok siebie, i sprawdza
; nowe wersje; tryb instalacji potrzebny przy aktualizacji (ten sam tryb, bez pytania)
Filename: "{app}\instalacja.ini"; Section: "SerwisDiag"; Key: "Wersja"; String: "{#AppVersion}"
Filename: "{app}\instalacja.ini"; Section: "SerwisDiag"; Key: "Tryb"; String: "wszyscy"; Check: IsAdminInstallMode
Filename: "{app}\instalacja.ini"; Section: "SerwisDiag"; Key: "Tryb"; String: "biezacy"; Check: not IsAdminInstallMode

[Icons]
Name: "{autoprograms}\{#AppTitle}"; Filename: "{app}\serwisdiag.exe"; WorkingDir: "{app}"; Comment: "Diagnostyka i naprawa systemu Windows"
Name: "{autodesktop}\{#AppTitle}"; Filename: "{app}\serwisdiag.exe"; WorkingDir: "{app}"; Comment: "Diagnostyka i naprawa systemu Windows"; Tasks: desktopicon

[Run]
; monitorowanie: autostart (instalacja dla wszystkich - zadanie Harmonogramu, wymaga uprawnień instalatora; dla mnie - wpis Run),
; potem monitor startuje jako użytkownik, który uruchomił instalator. Bez monitorowania: autostart tej kopii znika
; (monitor innej kopii programu, np. przenośnej, zostaje).
Filename: "{app}\serwisdiag.exe"; Parameters: "-EnableMonitor -NoStart"; Flags: runhidden waituntilterminated; Tasks: monitor; StatusMsg: "Włączanie monitorowania w tle..."
Filename: "{app}\serwisdiag.exe"; Parameters: "-Monitor"; Flags: nowait runasoriginaluser; Tasks: monitor
Filename: "{app}\serwisdiag.exe"; Parameters: "-DisableMonitor"; Flags: runhidden waituntilterminated; Tasks: not monitor
; aktualizacja z programu (/RELAUNCH=1): po instalacji program uruchamia się ponownie
Filename: "{app}\serwisdiag.exe"; Flags: nowait runasoriginaluser; Check: ShouldRelaunch
#if Registration
; dobrowolna rejestracja: okno programu jako użytkownik, który uruchomił instalator (bez UAC); pole tylko wtedy, gdy ten
; użytkownik jeszcze się nie rejestrował (także w wersji PowerShell - wspólny plik stanu)
Filename: "{app}\serwisdiag.exe"; Parameters: "-Register"; Description: "Zarejestruj program (dobrowolnie: imię i nazwisko, e-mail, telefon)"; Flags: postinstall skipifsilent runasoriginaluser waituntilterminated; Check: ShouldOfferRegistration
#endif
; program działa bez uprawnień administratora (w oknie: „Uruchom jako administrator”)
Filename: "{app}\serwisdiag.exe"; Description: "{cm:LaunchProgram,{#AppTitle}}"; Flags: nowait postinstall skipifsilent runasoriginaluser

[UninstallRun]
Filename: "{app}\serwisdiag.exe"; Parameters: "-DisableMonitor"; Flags: runhidden waituntilterminated; RunOnceId: "DisableMonitor"

[UninstallDelete]
; ustawienia serwisu; raporty klientów w Dokumentach zostają
Type: files; Name: "{app}\instalacja.ini"
Type: filesandordirs; Name: "{app}\config"
Type: dirifempty; Name: "{app}\raporty"
Type: dirifempty; Name: "{app}"

[Code]
function ShouldRelaunch: Boolean;
begin
  Result := ExpandConstant('{param:RELAUNCH|0}') = '1';
end;

// rejestracja zapisana przez program (wysłana albo czekająca na internet) - nie pytamy ponownie
function ShouldOfferRegistration: Boolean;
begin
  Result := not FileExists(ExpandConstant('{localappdata}\SerwisDiag\rejestracja.json'));
end;

// aktualizacja: monitor tej kopii programu kończy pracę przed podmianą plików; autostart zostaje - ustawia go sekcja
// [Run]. Monitor uruchomiony z wpisu Run albo przez instalator działa jako zwykły użytkownik, a z zadania Harmonogramu
// (instalacja dla wszystkich) - u administratora z pełnymi uprawnieniami: takiego zatrzyma tylko proces z uprawnieniami
// instalatora. Monitora, którego nie da się zatrzymać (np. innego użytkownika), zamyka Menedżer ponownego uruchamiania.
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Exe: String;
  Code: Integer;
begin
  Result := '';
  Exe := ExpandConstant('{app}\serwisdiag.exe');
  if FileExists(Exe) then
  begin
    ExecAsOriginalUser(Exe, '-StopMonitor', '', SW_HIDE, ewWaitUntilTerminated, Code);
    if IsAdminInstallMode then
      Exec(Exe, '-StopMonitor', '', SW_HIDE, ewWaitUntilTerminated, Code);
  end;
end;
