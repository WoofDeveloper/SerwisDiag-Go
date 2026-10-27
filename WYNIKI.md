# PowerShell czy Go – wyniki pomiarów SerwisDiag

Pomiary z 26/27.10.2026, narzędzie `sdbench.exe` (każde uruchomienie w osobnym obiekcie zadania – liczą się też
procesy potomne). Komputer: laptop Lenovo z Intel Core i5-12450HX, 16 GB RAM, dysk NVMe, Windows 11 Pro 26H2,
program uruchamiany **bez uprawnień administratora**. Wersje: PowerShell 1.5.0 (Windows PowerShell 5.1) i Go
(ta sama logika, Go 1.27.2). Wersje uruchamiane na zmianę; w tabelach mediana, w nawiasie najkrótszy i najdłuższy
pomiar.

- **Czas całkowity** – od startu programu do zapisania raportów.
- **Czas procesora** – ile procesor faktycznie pracował dla programu (wszystkie jego procesy).
- **Szczyt pamięci procesu** – największy zestaw roboczy głównego procesu; **zadania** – szczytowa pamięć
  zatwierdzona wszystkich procesów programu.
- **CPU usługi WMI** – czas procesora usługi WMI (WmiPrvSE.exe) w trakcie pomiaru: koszt zapytań, który
  Windows ponosi poza programem.

## Diagnostyka szybka (domyślna), z internetem – 3 pomiary

| Wersja | Czas całkowity [s] | Czas procesora [s] | Szczyt pamięci procesu [MB] | Szczyt pamięci zadania [MB] | Procesy | CPU usługi WMI [s] |
|---|---|---|---|---|---|---|
| PowerShell | 73,6 (71,9–80,7) | 28,7 (27,4–32,4) | 279 (272–288) | 243 (236–254) | 12 | 9,1 (8,9–10,8) |
| Go, testy równolegle | **21,9** (21,1–22,8) | **1,9** (1,8–2,0) | **46** | 104 | 4 | 5,9 (5,5–6,8) |
| Go, testy po kolei | 35,3 (30,9–36,0) | 2,8 (1,0–2,8) | 43 | 101 | 4 | 7,1 (6,8–7,2) |

## Diagnostyka szybka bez internetu (`-Offline`) – bez Windows Update i testów internetu

| Wersja | Czas całkowity [s] | Czas procesora [s] | Szczyt pamięci procesu [MB] | Szczyt pamięci zadania [MB] | Procesy | CPU usługi WMI [s] |
|---|---|---|---|---|---|---|
| PowerShell | 32,7 (28,2–37,1) | 10,4 (10,3–10,6) | 328 (324–332) | 294 (290–298) | 10 | 7,6 (4,2–11,1) |
| Go, testy równolegle | **12,4** (9,2–13,2) | **1,0** (0,9–1,3) | **40** | 104 | 4 | 7,3 (3,2–8,3) |
| Go, testy po kolei | 18,3 (14,4–18,6) | 0,9 (0,8–1,1) | 38 | 101 | 4 | 4,8 (2,7–5,2) |

Jeden pomiar wersji PowerShell z tego scenariusza odrzucono: w jego trakcie laptop zasnął (22:50–02:49).
Dlatego PowerShell ma tu 2 pomiary, nie 3.

## Diagnostyka pełna (`-Level Full`) – 2 pomiary

| Wersja | Czas całkowity [s] | Czas procesora [s] | Szczyt pamięci procesu [MB] | Szczyt pamięci zadania [MB] | Procesy | CPU usługi WMI [s] |
|---|---|---|---|---|---|---|
| PowerShell | 71,0 (68,7–73,2) | 15,1 (13,4–16,9) | 328 (312–343) | 296 (284–308) | 14 | 12,2 (11,8–12,6) |
| Go, testy równolegle | **42,6** (31,1–54,2) | **1,6** (1,2–2,0) | **49** | 104 | 4 | 10,0 (8,5–11,5) |
| Go, testy po kolei | 49,5 (43,9–55,2) | 2,2 (1,7–2,8) | 44 | 101 | 4 | 9,4 (9,1–9,8) |

Ten scenariusz wypadł zaraz po tym, jak Windows sam obudził laptopa o 02:49 na konserwację automatyczną –
usługa Windows Update była zajęta, stąd duży rozrzut czasów obu wersji. Czas procesora i pamięć są wiarygodne.

## Wnioski

1. **Procesor: 10–15 razy mniej pracy.** Wersja Go zużywa 1–2 s czasu procesora zamiast 10–29 s. W PowerShellu
   ten czas idzie na sam język: uruchomienie PowerShella i .NET, kompilację wstawek C# (csc.exe przy każdym
   starcie), potok obiektów, formatowanie opisu każdego zdarzenia przez `Get-WinEvent` i osobny proces
   PowerShell do wyszukiwania w Windows Update.
2. **Pamięć: 6–7 razy mniej.** 40–49 MB zamiast 279–343 MB w głównym procesie; 4 procesy zamiast 10–14.
3. **Czas: 2,5–3,4 razy szybciej** przy testach równoległych (szybka diagnostyka: 22 s zamiast 74 s), **1,8–2,1
   razy** przy testach po kolei – czyli w tej samej kolejności co w PowerShellu.
4. **Granica przyspieszenia to Windows, nie język.** Z ok. 22 s wersji Go większość to czekanie na system:
   wyszukiwanie sterowników w Windows Update (11–13 s, usługa Windows i serwer Microsoft), 5 s próbkowania
   obciążenia (celowo, jak w PowerShellu) i zapytania WMI – usługa WMI zużywa przy obu wersjach podobny czas
   procesora (Go trochę mniej, bo pyta tylko o potrzebne pola i część danych bierze z API Windows).
5. **Wyniki diagnostyki są te same**: przy porównaniu raportów (`sdcompare`) identycznych co do znaku 70 z 85
   wyników (szybka) i 75 z 90 (pełna), zero różnic statusu; pozostałe różnią się wartościami mierzonymi
   na żywo (zajętość RAM, ping, temperatura). Jedyna różnica w liczbie wyników: wersja Go pokazuje
   „.NET Framework”, którego wersja PowerShell z powodu błędu nie pokazuje (opis w README).
6. Plik programu: Go – jeden `serwisdiag.exe` 8,7 MB; PowerShell – 50 plików, 0,9 MB, ale korzysta
   z PowerShella i .NET wbudowanych w Windows (dlatego działa też na Windows 8).

Dla porównania pamięci w pracy ciągłej: monitor przy zegarze z wersji PowerShell (zainstalowana 1.5.0, proces
`powershell.exe`) zajmował 26.10.2026 o 22:5x **162 MB** pamięci prywatnej (wyniki monitora Go – niżej).

# Okno programu i monitor przy zegarze (27.10.2026)

Narzędzie `sdguibench.exe` (każde uruchomienie w osobnym obiekcie zadania, wersje na zmianę, 3 przebiegi; mediana,
w nawiasie najkrótszy i najdłuższy). PowerShell: okno WPF i monitor wersji 1.5.0 (`SerwisDiag.ps1`), Go:
`serwisdiag.exe` (natywne okno Windows). Bez uprawnień administratora.

## Start okna (bez diagnostyki): od uruchomienia do pokazania okna, pamięć po 5 s bezczynności

| Wersja | Czas do pokazania okna [s] | Czas procesora do tej chwili + 5 s [s] | Pamięć prywatna [MB] | Zestaw roboczy [MB] | Procesy |
|---|---|---|---|---|---|
| PowerShell (WPF) | 3,28 (3,14–4,67) | 1,88 (1,73–1,91) | 203 (177–203) | 221 (196–221) | 4 |
| Go | **0,09** (0,09–1,42) | **0,11** (0,09–0,12) | **59** (58–60) | **34** | 1 |

Pierwsze uruchomienie Go (1,42 s) – plik jeszcze nie był w pamięci podręcznej systemu.

## Autotest okna: szybka diagnostyka w oknie, zrzuty 5 stron, zamknięcie (`-SelfTest`)

| Wersja | Czas całkowity [s] | Czas procesora [s] | Szczyt pamięci zadania [MB] | Szczyt pamięci procesu [MB] | Procesy |
|---|---|---|---|---|---|
| PowerShell (WPF) | 57,5 (55,4–64,1) | 13,6 (13,5–14,0) | 362 (343–370) | 363 (354–364) | 12 |
| Go | **46,1** (29,6–58,4) | **2,6** (2,3–3,0) | **111** (110–112) | **76** (75–77) | 3 |

Rozrzut czasu Go to wyszukiwanie sterowników w Windows Update (usługa i serwery Microsoft) – ten sam test trwał
raz 30 s, raz 58 s. Czas procesora i pamięć są powtarzalne.

## Monitor: jedno sprawdzenie (start, 8 testów, okno wyników, zrzut, koniec; tryb testu)

| Wersja | Czas całkowity [s] | Czas procesora [s] | Szczyt pamięci zadania [MB] | Szczyt pamięci procesu [MB] | Procesy |
|---|---|---|---|---|---|
| PowerShell (WPF) | 14,1 (14,0–14,3) | 4,25 (3,92–4,31) | 298 (243–301) | 332 (264–336) | 4 |
| Go | **6,4** (6,1–6,4) | **0,19** (0,17–0,28) | **59** | **38** | 1 |

W obu czasach są 2 s czekania przed pierwszym sprawdzeniem. Obie wersje znalazły te same 2 problemy (identyczne
tytuły i wartości).

## Monitor w pracy ciągłej

Pamięć działającego monitora (Get-Process). Monitor Go uruchomiony na 5,5 minuty z osobnym plikiem stanu, bez
powiadomień; pierwsze sprawdzenie – 2 minuty po starcie, jak w wersji PowerShell.

| Monitor | Pamięć prywatna [MB] | Zestaw roboczy [MB] | Czas procesora [s] |
|---|---|---|---|
| PowerShell 1.5.0 (zainstalowany, działa od 26.10 18:21 – ok. 10 h, ok. 10 sprawdzeń) | 169,5 | 83,0 | 22,8 |
| Go – 60 s po starcie (przed pierwszym sprawdzeniem) | **47,6** | **16,8** | 0,03 |
| Go – po pierwszym sprawdzeniu (pomiary po 150, 240 i 330 s) | **52,3** | **27,5** | 0,13 |

Wersja PowerShell zużywa ok. 2 s procesora na sprawdzenie, Go ok. 0,1 s.

## Wnioski – okno i monitor

1. **Okno otwiera się 36 razy szybciej**: 0,09 s zamiast 3,3 s. Wersja PowerShell przy każdym starcie ładuje
   PowerShella, .NET i WPF, kompiluje wstawki C# i wczytuje program ze skryptów; Go – jeden plik i okno systemowe.
2. **Pamięć okna: 3–4 razy mniej** (59 zamiast 203 MB bezczynnie, 111 zamiast 362 MB w szczycie diagnostyki).
   Sama sterta Go to 3–5 MB – resztę zajmują biblioteki Windows (WMI, kontrolki, czcionki) i bufory rysowania.
3. **Czas procesora**: okno z diagnostyką 5 razy mniej (2,6 s zamiast 13,6 s), sprawdzenie monitora 22 razy mniej
   (0,19 s zamiast 4,25 s) – co godzinę, przez cały dzień pracy komputera.
4. **Monitor w tle: 3 razy mniej pamięci** (52 zamiast 170 MB prywatnej, 28 zamiast 83 MB roboczej) – jeden proces
   bez PowerShella, .NET i WPF trzymanych w pamięci przez cały dzień.

Warunki: podczas scenariusza „szybka” na komputerze działała gra (ok. 30% procesora, ~90% zajętej pamięci RAM),
więc oba programy mierzono w tych samych, niełatwych warunkach. Pomiar można powtórzyć:
`sdbench.exe -runs 3 -level Quick -out bench` (najlepiej na bezczynnym komputerze).

# Monitory w codziennej pracy (od 27.10.2026)

Od 27.10.2026 oba monitory startują przy logowaniu i działają obok siebie: zainstalowany SerwisDiag 1.5.0
(PowerShell, wpis „SerwisDiag Monitor”) i `C:\serwis-diag-go\serwisdiag.exe -Monitor` (wpis „SerwisDiag Go Monitor”),
każdy z własnym plikiem stanu i własnymi powiadomieniami. Porównanie: `tools\porownaj-monitory.ps1` (z `-Csv`
dopisuje pomiar do `bench\monitory.csv`). Pierwszy pomiar, 27.10 08:28 – PowerShell działa od 26.10 18:21
(14 h), Go od 08:21 (7 minut, jedno sprawdzenie):

| Monitor | Procesy | Pamięć prywatna [MB] | Szczyt pamięci prywatnej [MB] | Zestaw roboczy [MB] | Szczyt zestawu roboczego [MB] | Uchwyty |
|---|---|---|---|---|---|---|
| PowerShell 1.5.0 | 2 (powershell, conhost) | 174,5 | 280,1 | 91,9 | 288,3 | 929 |
| Go | 1 | **52,6** | **54,1** | **28,3** | **29,2** | **374** |

Czas procesora obu monitorów w tym samym okresie pokaże kolejne uruchomienie skryptu (od pomiaru z 08:28).
