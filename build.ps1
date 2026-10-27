# Buduje wersję Go programu SerwisDiag: serwisdiag.exe (okno programu, monitor przy zegarze, -NoGui: konsola)
# i serwisdiag-konsola.exe (tryb konsolowy - skrypty i pomiary), oba z ikoną, informacjami o wersji i manifestem
# (asInvoker, świadomość DPI per monitor v2, kontrolki Windows w wersji 6, długie ścieżki).
# Plik .syso jest tymczasowy - po budowaniu zostaje usunięty. Monitor Go działający w tle (serwisdiag.exe -Monitor)
# trzyma plik programu, więc na czas budowania zostaje zatrzymany, a potem uruchomiony ponownie.
#   powershell -ExecutionPolicy Bypass -File build.ps1          (pliki .exe w tym folderze)
#   powershell -ExecutionPolicy Bypass -File build.ps1 -Test    (najpierw testy jednostkowe)
param([switch]$Test, [string]$OutDir = $PSScriptRoot)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
if ($Test) {
    go test -short ./...
    if ($LASTEXITCODE -ne 0) { throw 'Testy nie przeszły.' }
}

function Stop-SdGoMonitor {
    # Zatrzymuje monitor Go uruchomiony z pliku $Exe (zdarzenie nazwane - jak -DisableMonitor, ale bez zmiany
    # autostartu). Wynik: $true, gdy monitor działał. Otwarte okno programu blokuje plik - wtedy przerywamy.
    param([string]$Exe)
    $procs = @(Get-CimInstance Win32_Process -Filter "Name='serwisdiag.exe'" | Where-Object { $_.ExecutablePath -eq $Exe })
    $windows = @($procs | Where-Object { $_.CommandLine -notmatch '(?i)\s-Monitor\b' })
    if ($windows.Count -gt 0) { throw ('Zamknij okno programu {0} (PID {1}) przed budowaniem.' -f $Exe, $windows[0].ProcessId) }
    $monitors = @($procs | Where-Object { $_.CommandLine -match '(?i)\s-Monitor\b' })
    if ($monitors.Count -eq 0) { return $false }
    try {
        $ev = [System.Threading.EventWaitHandle]::OpenExisting('Local\SerwisDiagGo.Monitor.Stop')
        [void]$ev.Set()
        $ev.Dispose()
    } catch { }
    foreach ($m in $monitors) {
        $p = Get-Process -Id $m.ProcessId -ErrorAction SilentlyContinue
        if ($p -and -not $p.WaitForExit(15000)) { throw ('Monitor Go (PID {0}) nie zakończył pracy.' -f $m.ProcessId) }
    }
    Write-Host 'Zatrzymano monitor Go na czas budowania.' -ForegroundColor DarkGray
    return $true
}

$gui = Join-Path $OutDir 'serwisdiag.exe'
$con = Join-Path $OutDir 'serwisdiag-konsola.exe'
$restartMonitor = $false
try {
    $restartMonitor = Stop-SdGoMonitor $gui
    go run github.com/tc-hib/go-winres@v0.3.3 make --arch amd64
    if ($LASTEXITCODE -ne 0) { throw 'go-winres nie utworzył zasobów.' }
    go build -trimpath -ldflags '-s -w -H windowsgui -X main.guiBuild=1' -o $gui .
    if ($LASTEXITCODE -ne 0) { throw 'Kompilacja (okno) nie powiodła się.' }
    go build -trimpath -ldflags '-s -w' -o $con .
    if ($LASTEXITCODE -ne 0) { throw 'Kompilacja (konsola) nie powiodła się.' }
    go build -trimpath -ldflags '-s -w' -o (Join-Path $OutDir 'sdbench.exe') ./cmd/sdbench
    go build -trimpath -ldflags '-s -w' -o (Join-Path $OutDir 'sdcompare.exe') ./cmd/sdcompare
    go build -trimpath -ldflags '-s -w' -o (Join-Path $OutDir 'sdguibench.exe') ./cmd/sdguibench
    foreach ($p in $gui, $con) {
        $f = Get-Item $p
        Write-Host ('Zbudowano: {0} ({1:N1} MB)' -f $f.FullName, ($f.Length / 1MB)) -ForegroundColor Green
    }
} finally {
    Remove-Item rsrc_windows_*.syso -ErrorAction SilentlyContinue
    if ($restartMonitor -and (Test-Path -LiteralPath $gui)) {
        Start-Process -FilePath $gui -ArgumentList '-Monitor'
        Write-Host 'Uruchomiono ponownie monitor Go.' -ForegroundColor DarkGray
    }
}
