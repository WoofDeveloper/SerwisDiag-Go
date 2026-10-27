# SerwisDiag (wersja Go) - pliki wydania do publikacji (plik celowo wylacznie ASCII).
#   powershell -ExecutionPolicy Bypass -File tools\Build-Release.ps1 [-OutDir <folder>] [-Iscc <sciezka do ISCC.exe>] [-SkipTests]
# Pliki programu (serwisdiag.exe, serwisdiag-konsola.exe) powstaja w folderze tymczasowym - pliki w folderze projektu
# i dzialajacy z nich monitor zostaja bez zmian. Wynik w dist\ (kazdy plik z suma SHA-256 obok, .sha256):
#   SerwisDiag-Go-<wersja>-instalator.exe - instalator (aktualizacja z programu pobiera go z wydania na GitHubie
#                                           i sprawdza sume przed instalacja),
#   SerwisDiag-Go-<wersja>.zip           - wersja przenosna (folder z programem, licencja i opisem).
param([string]$OutDir = '', [string]$Iscc = '', [switch]$SkipTests)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not $OutDir) { $OutDir = Join-Path $root 'dist' }
if (-not (Test-Path -LiteralPath $OutDir)) { [void](New-Item -ItemType Directory -Path $OutDir -Force) }
$OutDir = (Resolve-Path -LiteralPath $OutDir).Path

# 1) wersja z internal\diag\core.go; zasoby pliku (winres\winres.json) musza miec ten sam numer
$core = [System.IO.File]::ReadAllText((Join-Path $root 'internal\diag\core.go'))
if ($core -notmatch 'const Version = "(\d+\.\d+\.\d+)"') { Write-Host 'Nie znaleziono numeru wersji w internal\diag\core.go' -ForegroundColor Red; exit 1 }
$ver = $Matches[1]
$res = [System.IO.File]::ReadAllText((Join-Path $root 'winres\winres.json'))
$v4 = [regex]::Escape($ver + '.0')
if ($res -notmatch ('"file_version": "' + $v4 + '"') -or $res -notmatch ('"FileVersion": "' + [regex]::Escape($ver) + '"')) {
    Write-Host ('Numer wersji w winres\winres.json rozni sie od {0} (internal\diag\core.go) - popraw version, file_version, product_version, FileVersion i ProductVersion.' -f $ver) -ForegroundColor Red
    exit 1
}

# 2) Inno Setup 7
if (-not $Iscc) {
    foreach ($c in @((Join-Path $env:LOCALAPPDATA 'Programs\Inno Setup 7\ISCC.exe'), (Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 7\ISCC.exe'), (Join-Path $env:ProgramFiles 'Inno Setup 7\ISCC.exe'))) {
        if (Test-Path -LiteralPath $c) { $Iscc = $c; break }
    }
}
if (-not $Iscc) { Write-Host 'Nie znaleziono Inno Setup 7 (ISCC.exe). Pobierz go z https://jrsoftware.org/isdl.php albo podaj -Iscc.' -ForegroundColor Red; exit 1 }

# 3) testy i pliki programu w folderze tymczasowym (zasoby: ikona, wersja, manifest)
$name = 'SerwisDiag-Go-' + $ver
$work = Join-Path ([System.IO.Path]::GetTempPath()) ('sd-go-wydanie-' + [guid]::NewGuid().ToString('N'))
$stage = Join-Path $work $name
[void](New-Item -ItemType Directory -Path $stage -Force)
$ok = $false
Push-Location $root
try {
    if (-not $SkipTests) {
        go test -short ./...
        if ($LASTEXITCODE -ne 0) { throw 'Testy nie przeszly.' }
    }
    go run github.com/tc-hib/go-winres@v0.3.3 make --arch amd64
    if ($LASTEXITCODE -ne 0) { throw 'go-winres nie utworzyl zasobow.' }
    go build -trimpath -ldflags '-s -w -H windowsgui -X main.guiBuild=1' -o (Join-Path $stage 'serwisdiag.exe') .
    if ($LASTEXITCODE -ne 0) { throw 'Kompilacja (okno) nie powiodla sie.' }
    go build -trimpath -ldflags '-s -w' -o (Join-Path $stage 'serwisdiag-konsola.exe') .
    if ($LASTEXITCODE -ne 0) { throw 'Kompilacja (konsola) nie powiodla sie.' }
    $ok = $true
} catch {
    Write-Host $_.Exception.Message -ForegroundColor Red
} finally {
    Remove-Item (Join-Path $root 'rsrc_windows_*.syso') -ErrorAction SilentlyContinue
    Pop-Location
}
if (-not $ok) { Remove-Item -LiteralPath $work -Recurse -Force; exit 1 }

# 4) licencja i opis: w folderze programu, a licencja z BOM (bez niego kreator pokazalby polskie znaki jako tekst ANSI)
#    takze w kreatorze instalacji
Copy-Item -LiteralPath (Join-Path $root 'LICENSE') -Destination (Join-Path $stage 'LICENSE')
Copy-Item -LiteralPath (Join-Path $root 'README.md') -Destination (Join-Path $stage 'README.md')
$lic = Join-Path $work 'LICENSE-instalator.txt'
[System.IO.File]::WriteAllText($lic, [System.IO.File]::ReadAllText((Join-Path $root 'LICENSE')), (New-Object System.Text.UTF8Encoding($true)))

function Write-SdSha256 {
    param([string]$Path)
    $h = (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLower()
    [System.IO.File]::WriteAllText(($Path + '.sha256'), ('{0}  {1}' -f $h, (Split-Path -Leaf $Path)) + "`r`n", (New-Object System.Text.ASCIIEncoding))
    return $h
}

# 5) wersja przenosna: ZIP z folderem SerwisDiag-Go-<wersja> w srodku (bez raportow, ustawien i dziennikow)
#    Wpisy dodawane po kolei: ZipFile.CreateFromDirectory w Windows PowerShell 5.1 zapisuje sciezki z ukosnikiem
#    wstecznym, a format ZIP wymaga "/" (inne programy niz Eksplorator rozpakowalyby je zle).
$zip = Join-Path $OutDir ($name + '.zip')
if (Test-Path -LiteralPath $zip) { Remove-Item -LiteralPath $zip -Force }
Add-Type -AssemblyName System.IO.Compression, System.IO.Compression.FileSystem
$fs = [System.IO.File]::Open($zip, [System.IO.FileMode]::CreateNew)
try {
    $za = New-Object System.IO.Compression.ZipArchive($fs, [System.IO.Compression.ZipArchiveMode]::Create)
    try {
        foreach ($f in @(Get-ChildItem -LiteralPath $stage -File | Sort-Object Name)) {
            [void][System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile($za, $f.FullName, ($name + '/' + $f.Name), [System.IO.Compression.CompressionLevel]::Optimal)
        }
    } finally { $za.Dispose() }
} finally { $fs.Dispose() }
$zipHash = Write-SdSha256 $zip
Write-Host ('Paczka przenosna: {0}' -f $zip) -ForegroundColor Green
Write-Host ('Rozmiar: {0:N0} KB, SHA-256: {1}' -f ((Get-Item -LiteralPath $zip).Length / 1KB), $zipHash)

# 6) instalator; pole "Zarejestruj program" na koncu - tylko gdy program ma adres formularza rejestracji
$setup = Join-Path $OutDir ($name + '-instalator.exe')
if (Test-Path -LiteralPath $setup) { Remove-Item -LiteralPath $setup -Force }
$regSrc = [System.IO.File]::ReadAllText((Join-Path $root 'internal\diag\registration.go'))
$regDef = '/DRegistration=' + $(if ($regSrc -match 'FormURL:\s*"https://') { '1' } else { '0' })
& $Iscc '/Q' ('/DAppVersion=' + $ver) ('/DSourceDir=' + $stage) ('/DLicensePath=' + $lic) ('/DIconPath=' + (Join-Path $root 'winres\icon.ico')) ('/DOutDir=' + $OutDir) $regDef (Join-Path $root 'tools\installer\SerwisDiagGo.iss')
if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $setup)) {
    Write-Host ('Kompilacja instalatora nie powiodla sie (kod {0}).' -f $LASTEXITCODE) -ForegroundColor Red
    Remove-Item -LiteralPath $work -Recurse -Force
    exit 1
}
$sh = Write-SdSha256 $setup
Write-Host ('Instalator: {0}' -f $setup) -ForegroundColor Green
Write-Host ('Rozmiar: {0:N0} KB, SHA-256: {1}' -f ((Get-Item -LiteralPath $setup).Length / 1KB), $sh)
Remove-Item -LiteralPath $work -Recurse -Force
exit 0
