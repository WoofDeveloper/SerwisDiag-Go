# Porównuje monitory przy zegarze działające obok siebie: wersję PowerShell (powershell.exe ... SerwisDiag.ps1
# -Monitor, zwykle zainstalowany SerwisDiag) i wersję Go (serwisdiag.exe -Monitor). Liczniki procesów obejmują cały
# czas od ich startu (zwykle od zalogowania), więc wystarczy uruchomić skrypt np. pod koniec dnia. Wliczane są też
# procesy potomne monitora działające w chwili pomiaru - wersja PowerShell ma stale conhost.exe (ukryte okno konsoli).
# Gdy monitory wystartowały o różnych porach, skrypt pokazuje czas procesora obu od ostatniego pomiaru zapisanego
# z -Csv (ten sam okres dla obu procesów).
#   powershell -ExecutionPolicy Bypass -File tools\porownaj-monitory.ps1
#   powershell -ExecutionPolicy Bypass -File tools\porownaj-monitory.ps1 -Csv   (dopisuje pomiar do bench\monitory.csv)
param([switch]$Csv, [string]$CsvPath = (Join-Path (Split-Path -Parent $PSScriptRoot) 'bench\monitory.csv'))
$ErrorActionPreference = 'Stop'
$dataDir = Join-Path $env:LOCALAPPDATA 'SerwisDiag'

function Read-SdMonitorState {
    # Plik stanu monitora (monitor.json / monitor-go.json); $null przed pierwszym sprawdzeniem.
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    $s = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json
    $last = $s.lastCheck
    # PowerShell 7 sam zamienia datę na [datetime], Windows PowerShell 5.1 zostawia tekst w formacie 's'
    if ($last -and $last -isnot [datetime]) { $last = [datetime]::ParseExact([string]$last, 's', $null) }
    [pscustomobject]@{ LastCheck = $last; Interval = [int]$s.interval; Problems = ($s.problems | Measure-Object).Count }
}

function Format-SdInterval {
    param([int]$Minutes)
    if ($Minutes -ge 1440) { return 'raz na dobę' }
    if ($Minutes -eq 60) { return 'co godzinę' }
    if ($Minutes % 60 -eq 0) { return 'co {0} h' -f ($Minutes / 60) }
    'co {0} min' -f $Minutes
}

function Measure-SdMonitor {
    # Liczniki jednego monitora: proces główny i jego procesy potomne; Running = $false, gdy monitor nie działa.
    param([string]$Name, $Process, [string]$Version, [string]$StateFile)
    $main = if ($Process) { Get-Process -Id $Process.ProcessId -ErrorAction SilentlyContinue }
    if (-not $main) { return [pscustomobject]@{ Name = $Name; Version = $Version; Running = $false } }
    # tylko procesy utworzone po monitorze - numer PID zakończonego procesu mógł zostać użyty ponownie
    $kids = @(Get-CimInstance Win32_Process -Filter "ParentProcessId=$($Process.ProcessId)" |
        Where-Object { $_.CreationDate -ge $Process.CreationDate } |
        ForEach-Object { Get-Process -Id $_.ProcessId -ErrorAction SilentlyContinue })
    $procs = @($main) + $kids
    $mb = { param($Prop) ($procs | Measure-Object -Property $Prop -Sum).Sum / 1MB }
    $hours = ((Get-Date) - $main.StartTime).TotalHours
    $cpu = ($procs | ForEach-Object { $_.TotalProcessorTime.TotalSeconds } | Measure-Object -Sum).Sum
    $st = Read-SdMonitorState $StateFile
    [pscustomobject]@{
        Name = $Name; Version = $Version; Running = $true; Pid = $main.Id
        Processes = '{0} ({1})' -f $procs.Count, (($procs | ForEach-Object ProcessName) -join ', ')
        Start = $main.StartTime; Hours = $hours
        PrivateMB = & $mb PrivateMemorySize64; PeakPrivateMB = & $mb PeakPagedMemorySize64
        WorkingSetMB = & $mb WorkingSet64; PeakWorkingSetMB = & $mb PeakWorkingSet64
        CpuS = $cpu; CpuPerHourS = $cpu / [math]::Max($hours, 0.01)
        Threads = ($procs | ForEach-Object { $_.Threads.Count } | Measure-Object -Sum).Sum
        Handles = ($procs | Measure-Object -Property HandleCount -Sum).Sum
        LastCheck = if ($st) { $st.LastCheck } else { $null }
        Problems = if ($st -and $st.LastCheck) { $st.Problems } else { $null }
        Interval = if ($st) { Format-SdInterval $st.Interval } else { Format-SdInterval 60 }
    }
}

$all = @(Get-CimInstance Win32_Process -Filter "Name='powershell.exe' OR Name='pwsh.exe' OR Name='serwisdiag.exe'" | Sort-Object CreationDate)
$psProc = $all | Where-Object { $_.Name -ne 'serwisdiag.exe' -and $_.CommandLine -match '(?i)SerwisDiag\.ps1"?\s+-Monitor\b' } | Select-Object -First 1
$goProc = $all | Where-Object { $_.Name -eq 'serwisdiag.exe' -and $_.CommandLine -match '(?i)\s-Monitor\b' } | Select-Object -First 1

$psVersion = ''
if ($psProc -and $psProc.CommandLine -match '(?i)-File\s+"?([^"]*SerwisDiag\.ps1)') {
    $core = Join-Path (Split-Path -Parent $Matches[1]) 'lib\Core.ps1'
    if ((Test-Path -LiteralPath $core) -and ((Get-Content -LiteralPath $core -Raw -Encoding UTF8) -match "SdVersion\s*=\s*'([^']+)'")) { $psVersion = $Matches[1] }
}
$goVersion = ''
if ($goProc -and $goProc.ExecutablePath) { $goVersion = (Get-Item -LiteralPath $goProc.ExecutablePath).VersionInfo.ProductVersion }

$ps = Measure-SdMonitor 'PowerShell' $psProc $psVersion (Join-Path $dataDir 'monitor.json')
$go = Measure-SdMonitor 'Go' $goProc $goVersion (Join-Path $dataDir 'monitor-go.json')

# kolumna „PS / Go”: ile razy więcej zużywa wersja PowerShell
$rows = @(
    @{ Label = 'Wersja programu'; Prop = 'Version' }
    @{ Label = 'Procesy'; Prop = 'Processes' }
    @{ Label = 'Działa od'; Prop = 'Start'; Fmt = '{0:dd.MM.yyyy HH:mm}' }
    @{ Label = 'Czas działania [h]'; Prop = 'Hours'; Fmt = '{0:N1}' }
    @{ Label = 'Pamięć prywatna [MB]'; Prop = 'PrivateMB'; Fmt = '{0:N1}'; Ratio = $true }
    @{ Label = 'Szczyt pamięci prywatnej [MB]'; Prop = 'PeakPrivateMB'; Fmt = '{0:N1}'; Ratio = $true }
    @{ Label = 'Zestaw roboczy [MB]'; Prop = 'WorkingSetMB'; Fmt = '{0:N1}'; Ratio = $true }
    @{ Label = 'Szczyt zestawu roboczego [MB]'; Prop = 'PeakWorkingSetMB'; Fmt = '{0:N1}'; Ratio = $true }
    @{ Label = 'Czas procesora [s]'; Prop = 'CpuS'; Fmt = '{0:N1}' }
    @{ Label = 'Czas procesora na godzinę [s]'; Prop = 'CpuPerHourS'; Fmt = '{0:N2}'; Ratio = $true }
    @{ Label = 'Wątki'; Prop = 'Threads'; Fmt = '{0}' }
    @{ Label = 'Uchwyty'; Prop = 'Handles'; Fmt = '{0}' }
    @{ Label = 'Ostatnie sprawdzenie'; Prop = 'LastCheck'; Fmt = '{0:dd.MM HH:mm}' }
    @{ Label = 'Problemy'; Prop = 'Problems'; Fmt = '{0}' }
    @{ Label = 'Sprawdzanie'; Prop = 'Interval' }
)
$both = $ps.Running -and $go.Running
$sameStart = $both -and [math]::Abs(($ps.Start - $go.Start).TotalMinutes) -le 10
$line = '{0,-31} {1,-26} {2,-26} {3}'
'Monitory przy zegarze - {0:dd.MM.yyyy HH:mm}' -f (Get-Date)
''
$line -f '', 'PowerShell', 'Go', 'PS / Go'
foreach ($row in $rows) {
    $cells = foreach ($m in $ps, $go) {
        $v = if ($m.Running -or $row.Prop -eq 'Version') { $m.($row.Prop) }
        if ($null -eq $v -or "$v" -eq '') { '-' } elseif ($row.Fmt) { $row.Fmt -f $v } else { "$v" }
    }
    # czas procesora na godzinę da się zestawić tylko przy wspólnym starcie (np. obu przy logowaniu)
    $ratio = ''
    if ($row.Ratio -and $both -and $go.($row.Prop) -gt 0 -and ($row.Prop -ne 'CpuPerHourS' -or $sameStart)) {
        $ratio = '{0:N1}×' -f ($ps.($row.Prop) / $go.($row.Prop))
    }
    $line -f $row.Label, $cells[0], $cells[1], $ratio
}
''
foreach ($m in $ps, $go) { if (-not $m.Running) { "Monitor $($m.Name) nie działa." } }

# Ten sam okres dla obu: zmiana od najstarszego pomiaru zapisanego z -Csv, w którym są te same procesy
$history = @(if (Test-Path -LiteralPath $CsvPath) { Import-Csv -LiteralPath $CsvPath -Delimiter ';' -Encoding UTF8 })
$key = { param($m) '{0}|{1}|{2}' -f $m.Name, $m.Pid, $m.Start.ToString('yyyy-MM-dd HH:mm:ss') }
$base = if ($both) {
    $history | Group-Object Pomiar | Where-Object {
        $keys = @($_.Group | ForEach-Object { '{0}|{1}|{2}' -f $_.Monitor, $_.PID, $_.Start })
        ($keys -contains (& $key $ps)) -and ($keys -contains (& $key $go))
    } | Select-Object -First 1
}
if ($base) {
    $since = [datetime]::ParseExact($base.Name, 'yyyy-MM-dd HH:mm:ss', $null)
    $hours = ((Get-Date) - $since).TotalHours
    if ($hours -lt 1) {
        'Pomiar odniesienia zapisano o {0:HH:mm} - czas procesora obu monitorów w tym samym okresie skrypt pokaże' -f $since
        'po co najmniej godzinie (monitory sprawdzają system co godzinę).'
    } else {
        $cpu = foreach ($m in $ps, $go) {
            $r = $base.Group | Where-Object { $_.Monitor -eq $m.Name -and $_.PID -eq "$($m.Pid)" } | Select-Object -First 1
            [math]::Max(0.0, $m.CpuS - [double]::Parse($r.CpuS, [cultureinfo]::CurrentCulture)) # 0.0: Max(int, int) obcina ułamki
        }
        $cmp = if ($cpu[1] -gt 0) { ' (PS / Go: {0:N1}×)' -f ($cpu[0] / $cpu[1]) }
        'Od pomiaru {0:dd.MM HH:mm} ({1:N1} h) czas procesora: PowerShell {2:N1} s, Go {3:N1} s{4}.' -f $since, $hours, $cpu[0], $cpu[1], $cmp
    }
} elseif ($both -and -not $sameStart) {
    'Monitory wystartowały o różnych porach, więc czasu procesora od startu nie da się zestawić wprost.'
    if ($Csv) { 'Ten pomiar jest punktem odniesienia: uruchom skrypt ponownie za kilka godzin - pokaże czas procesora obu' }
    else { 'Zapisz pomiar (-Csv) i uruchom skrypt ponownie za kilka godzin - pokaże czas procesora obu' }
    'monitorów w tym samym okresie.'
}
'Liczone są procesy monitora od ich startu (z procesami potomnymi działającymi teraz). Nie są liczone krótkie procesy'
'pomocnicze, które już się zakończyły (np. kompilator C# przy starcie wersji PowerShell), ani praca usługi WMI.'

if ($Csv) {
    # liczby w formacie bieżącej kultury (przecinek), średnik jako separator - plik otwiera się wprost w Excelu
    $now = Get-Date
    $out = foreach ($m in @($ps, $go) | Where-Object Running) {
        [pscustomobject]@{
            Pomiar = $now.ToString('yyyy-MM-dd HH:mm:ss'); Monitor = $m.Name; Wersja = $m.Version; PID = $m.Pid
            Start = $m.Start.ToString('yyyy-MM-dd HH:mm:ss'); CzasDzialaniaH = '{0:0.00}' -f $m.Hours
            PrywatnaMB = '{0:0.0}' -f $m.PrivateMB; SzczytPrywatnejMB = '{0:0.0}' -f $m.PeakPrivateMB
            RoboczyMB = '{0:0.0}' -f $m.WorkingSetMB; SzczytRoboczegoMB = '{0:0.0}' -f $m.PeakWorkingSetMB
            CpuS = '{0:0.000}' -f $m.CpuS; CpuNaGodzineS = '{0:0.000}' -f $m.CpuPerHourS
            Watki = $m.Threads; Uchwyty = $m.Handles
            OstatnieSprawdzenie = if ($m.LastCheck) { $m.LastCheck.ToString('yyyy-MM-dd HH:mm:ss') } else { '' }
            Problemy = $m.Problems
        }
    }
    if ($out) {
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $CsvPath) | Out-Null
        $enc = if ($PSVersionTable.PSVersion.Major -ge 6) { 'utf8BOM' } else { 'UTF8' }
        $out | Export-Csv -LiteralPath $CsvPath -Append -NoTypeInformation -Delimiter ';' -Encoding $enc
        ''
        'Dopisano pomiar do {0}' -f [System.IO.Path]::GetFullPath($CsvPath)
    }
}
