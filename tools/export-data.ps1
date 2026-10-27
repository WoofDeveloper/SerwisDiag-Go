# Przenosi bazę wiedzy z wersji PowerShell (C:\serwis-diag\data\*.psd1) do plików JSON wbudowywanych w program Go
# (internal\diag\data). Uruchom po każdej zmianie danych w wersji PowerShell, potem przebuduj program.
param([string]$Source = 'C:\serwis-diag\data')
$ErrorActionPreference = 'Stop'
$dest = Join-Path $PSScriptRoot '..\internal\diag\data'
New-Item -ItemType Directory -Force -Path $dest | Out-Null

function ConvertTo-PlainData {
    # JSON wymaga kluczy tekstowych (w lifecycle.psd1 lata ESU mają klucze liczbowe).
    param($Value)
    if ($Value -is [hashtable]) {
        $h = [ordered]@{}
        foreach ($k in ($Value.Keys | Sort-Object { [string]$_ })) { $h[[string]$k] = ConvertTo-PlainData $Value[$k] }
        return $h
    }
    if ($Value -is [array]) { return , @($Value | ForEach-Object { ConvertTo-PlainData $_ }) }
    return $Value
}

foreach ($f in Get-ChildItem -LiteralPath $Source -Filter '*.psd1') {
    $text = [System.IO.File]::ReadAllText($f.FullName, [System.Text.Encoding]::UTF8)
    $data = ConvertTo-PlainData (& ([scriptblock]::Create($text)))
    $json = ConvertTo-Json -InputObject $data -Depth 8 -Compress
    $out = Join-Path $dest ($f.BaseName + '.json')
    [System.IO.File]::WriteAllText($out, $json, (New-Object System.Text.UTF8Encoding($false)))
    Write-Host ('{0} -> {1}' -f $f.Name, $out)
}
