# SerwisDiag – wersja Go

Przeniesienie programu **SerwisDiag 1.5.0** (wersja PowerShell w `C:\serwis-diag`) do języka **Go**: silnik
diagnostyczny, raporty, okno programu, monitor przy zegarze, naprawy, aktualizacje i instalator. Program nie potrzebuje PowerShella, .NET, WPF, C#
ani żadnych bibliotek – z Windows rozmawia bezpośrednio przez jego API: COM/WMI, Event Log API, rejestr,
IP Helper, PDH, DISM API, menedżer usług, Harmonogram zadań, a okna rysuje przez user32/gdi32 i kontrolki
systemowe (comctl32).

Dwa pliki z tego samego kodu (po ok. 10 MB):

- **`serwisdiag.exe`** – okno programu (bez parametrów), monitor przy zegarze (`-Monitor`), okno rejestracji (`-Register`), tryb konsolowy (`-NoGui`),
- **`serwisdiag-konsola.exe`** – zawsze tryb konsolowy (skrypty, pomiary).

Autor: Kacper Suliński - INDUSIP.PL · licencja MIT · cel: test wydajności obu wersji.

## Czy da się przepisać program tylko na Go?

Tak. Wszystko, co wersja PowerShell robi poleceniami `Get-CimInstance`, `Get-WinEvent`, `Get-PhysicalDisk`,
`Get-ScheduledTask`, `Repair-WindowsImage` czy wstawkami C#, Go wywołuje bezpośrednio z Windows:

| Wersja PowerShell | Wersja Go |
|---|---|
| `Get-CimInstance` (WMI) | interfejsy COM `IWbemLocator` / `IWbemServices` wywoływane z Go (`internal/win/wmi.go`) |
| `Get-WinEvent` | Windows Event Log API (`wevtapi.dll`); opis zdarzenia tylko wtedy, gdy test go potrzebuje |
| `New-Object -ComObject` (Windows Update, Harmonogram) | OLE Automation (`IDispatch`) w Go (`internal/win/oleauto.go`) |
| wstawki C# (NVMe, PDH, test dysku, rozmiar katalogów) | wywołania API z Go |
| `Get-PhysicalDisk`, `Get-Disk`, `Get-Partition` | te same klasy WMI (`root\Microsoft\Windows\Storage`) |
| `Win32_Process`, `Win32_Service`, `Win32_UserAccount` | `NtQuerySystemInformation`, menedżer usług, NetAPI – kilkanaście razy szybciej |
| `Get-NetRoute`, `Get-NetTCPConnection`, `Ping` | IP Helper API (`GetIpForwardTable2`, `GetExtendedTcpTable`, `IcmpSendEcho`) |
| `Repair-WindowsImage -CheckHealth` | DISM API (`dismapi.dll`) |
| `powercfg /getactivescheme` | `PowerGetActiveScheme` |

**Ograniczenie:** Go od wersji 1.21 działa tylko na **Windows 10 i 11** (to był powód, dla którego
24.10.2026 zostaliśmy przy PowerShellu). Na Windows 8 / 8.1 ta wersja się nie uruchomi.

## Co jest przeniesione (stan na 2026-10-27)

- **wszystkie 62 testy** wersji 1.5.0 – 57 diagnostycznych (system, sprzęt, dyski z SMART/NVMe, zasilanie,
  wydajność, zdarzenia, sterowniki z Windows Update, bezpieczeństwo, aktualizacje, sieć, oprogramowanie)
  i 5 obciążeniowych (`tt.disk`, `tt.cpu`, `tt.net`, `tt.volscan`, `tt.dism`) – z tymi samymi progami i tekstami,
- **baza wiedzy** (`data\*.psd1` → `internal/diag/data/*.json`, wbudowana w plik programu),
- **raporty**: techniczny i dla klienta (HTML jak w wersji PowerShell), JSON (zgodny – wersja PowerShell
  może go wczytać do porównania przed/po), podsumowanie TXT, raport anonimowy, PDF przez Edge,
- **tryb konsolowy** z tymi samymi parametrami (`-Level`, `-TestIds`, `-OutDir`, `-Ticket`, `-Customer`,
  `-Technician`, `-Note`, `-Offline`, `-Pdf`, `-Anonymous`, `-Open`, `-Elevate`, `-Pause`) i kodami wyjścia,
- **okno programu** (od 2026-10-27) – układ i kolory jak w oknie WPF: nagłówek z systemem i uprawnieniami, banery
  (brak administratora + „Uruchom jako administrator”, nieaktualna baza wiedzy), dane zlecenia, szybka/pełna
  diagnostyka, testy dodatkowe, tryb bez internetu, pasek postępu i **wyniki na żywo** w trakcie skanowania,
  przerwanie (test, który nie reaguje, po 8 s zostaje w tle, a okno kończy z wynikami częściowymi), menu kategorii
  z kolorową kropką i liczbą problemów, podsumowanie (werdykt, liczniki, dane komputera, karty problemów
  z zaleceniami), strony kategorii (lista wyników z plakietkami statusu, szczegóły i zalecenie wybranego wyniku,
  dane szczegółowe w tabelach), narzędzia serwisowe, raporty techniczny i dla klienta, PDF, folder raportów,
  podsumowanie do schowka, raport anonimowy, okna „Ustawienia serwisu” i „O programie”, test ekranu i klawiatury,
  dopasowanie do ekranu i skalowania (125–200%, także po przeniesieniu okna na inny monitor),
- **monitor przy zegarze** (`-Monitor`) – ta sama ikona co w 1.5.0 (plakietka z linią pulsu, kółko z wykrzyknikiem
  przy ostrzeżeniach i problemach krytycznych), co godzinę / 4 godziny / dobę 8 lekkich testów (dyski, woluminy,
  awarie, błędy sprzętu, urządzenia, antywirus, zapora, Windows Update), powiadomienie tylko o nowych albo
  pogorszonych problemach i nowych zdarzeniach, okno wyników, menu (pokaż, sprawdź teraz, otwórz program,
  częstotliwość, zakończ, wyłącz), autostart przy logowaniu (`-EnableMonitor`, `-DisableMonitor` albo w ustawieniach;
  w instalacji dla wszystkich użytkowników – zadanie Harmonogramu),
- **naprawy i ich cofanie** (od 2026-10-27) – wszystkie 28 napraw wersji 1.5.0 z tymi samymi warunkami i tekstami:
  system (ochrona systemu, WMI, DISM, SFC), Windows Update (usługa, zasady, reset składników), sterowniki z Windows
  Update, bezpieczeństwo (Defender, definicje, zapora, UAC, SMB1, Zdalny rejestr, Winlogon, IFEO, AppInit_DLLs),
  dysk i system (zrzuty pamięci, TRIM, plik wymiany, pliki tymczasowe), sieć (DNS, zegar, proxy, Winsock),
  autostart (podejrzane i zbędne wpisy) i deinstalacja programów niechcianych. Okno „Napraw problemy...” (lista
  z zalecanymi zaznaczonymi, „Tego program nie naprawi”, punkt przywracania, postęp, wynik, „Diagnostyka ponownie”),
  konsola `-RepairList`, `-Repair`, `-RepairIds`, dziennik napraw (TXT i JSON obok raportów) oraz cofanie zmian
  ustawień z dziennika (rejestr, typ uruchamiania usług, zapora, SMB1): okno „Cofnij naprawy z dziennika” (menu
  „Więcej” i narzędzia serwisowe) i `-UndoRepair <plik | ostatni>`. **Dzienniki napraw są wymienne z wersją
  PowerShell** (te same pola i ścieżki rejestru w zapisie PowerShella) – zmiany wprowadzone jedną wersją cofa druga.
  Zamiast poleceń PowerShella naprawy wywołują Windows bezpośrednio: metody WMI (`SystemRestore.Enable`,
  `MSFT_MpPreference.Set`, `MSFT_MpSignature.Update`, `MSFT_SmbServerConfiguration.SetConfiguration`,
  `Win32_ComputerSystem.AutomaticManagedPagefile`), zaporę przez `HNetCfg.FwPolicy2`, usługi przez menedżer usług,
  instalację sterowników przez `Microsoft.Update.Session`, punkt przywracania przez `SRSetRestorePointW`.
- **porównanie przed/po naprawie** (od 2026-10-27) – raport z naprawy techniczny i dla klienta: rozwiązane,
  złagodzone, pogorszone, bez zmian, nowe, bez nowych zdarzeń (stare wpisy dzienników Windows), niesprawdzone
  ponownie, uwagi (inny komputer, kolejność plików, tryb, uprawnienia, tryb bez internetu) i wykonane czynności.
  Przycisk „Porównaj przed/po” (podpowiada raport bieżącej diagnostyki i poprzedni raport tego komputera, naprawy
  wykonane w programie wpisują się same), konsola `-CompareWith`, `-CompareBefore` / `-CompareAfter`, `-WorkDone`.
  Raporty JSON obu wersji są wymienne, a **raport porównania jest bajt w bajt taki sam jak z wersji PowerShell**
  (sprawdzone na tej samej parze raportów, z każdym rodzajem zmiany).
- **sterowniki i DISM z płyty/ISO** (od 2026-10-27) – narzędzia serwisowe, grupa „Sterowniki”: okno „Sterowniki
  z Windows Update” (wyszukiwanie w tle, zaznaczone zalecane i naprawiające urządzenia z błędem, punkt przywracania,
  pobieranie i instalacja z postępem), „Brakujące sterowniki (Katalog Microsoft)” (urządzenia z błędami, wyszukiwanie
  po identyfikatorze sprzętu, kopiowanie identyfikatora) i „Strona sterowników producenta” (numer seryjny albo model
  w schowku); konsola `-InstallDrivers`. „DISM RestoreHealth z płyty/ISO”: podłącza obraz ISO, dobiera edycję
  z `install.wim` / `install.esd` do systemu (wydanie, edycja, architektura, język) i uruchamia DISM z `/Source`
  i `/LimitAccess`. Edycje odczytuje wprost z opisu obrazu w pliku (zasób XML nagłówka WIM) zamiast przez moduł DISM
  (`Get-WindowsImage`), a obraz ISO podłącza funkcjami dysków wirtualnych Windows (jak Eksplorator) – obie rzeczy
  działają bez uprawnień administratora (sam DISM ich wymaga).
- **kopia przed reinstalacją Windows** (od 2026-10-27) – okno (menu „Więcej” i narzędzia serwisowe) i konsola
  `-Backup <folder> [-BackupInclude ...]`: eksport sterowników producentów przez DISM (wcześniej sprawdzenie długości
  ścieżek i miejsca na nośniku), lista sterowników i programów (CSV dla Excela, HTML do wydruku), dane komputera
  i klucz Windows z BIOS/UEFI (`system.txt`), profile Wi-Fi z hasłami, klucze odzyskiwania BitLocker i instrukcja
  `CZYTAJ.txt`. Sprawdzone bez administratora: `CZYTAJ.txt` i `system.txt` bajt w bajt jak z wersji PowerShell,
  wyliczenie eksportu sterowników takie samo (112 pakietów, 8,8 GB, najdłuższa ścieżka 147 znaków).

- **aktualizacje z GitHuba** (od 2026-10-27) – jak w wersji PowerShell: „Sprawdź nową wersję” w oknie „O programie”,
  w wersji zainstalowanej sprawdzanie raz na dobę w tle (okno i monitor; do wyłączenia w ustawieniach), pasek
  „Dostępna nowa wersja” z „Co nowego” i „Zaktualizuj” (przenośna: „Pobierz”), powiadomienie monitora raz na wersję
  i konsola `-Update`. Aktualizacja pobiera instalator w tle, sprawdza sumę SHA-256 z pliku `.sha256` wydania
  i numer wersji zapisany w pliku, uruchamia go w tym samym trybie (dla wszystkich / dla mnie), a program się zamyka
  i wraca po instalacji. Wydania wersji Go są w osobnym repozytorium **https://github.com/WoofDeveloper/SerwisDiag-Go**
  (`diag.ProjectURL`) – wersja PowerShell czyta najnowsze wydanie swojego repozytorium, więc wydanie samej wersji Go
  w tym samym repozytorium zepsułoby jej aktualizacje. Pliki wydania: `SerwisDiag-Go-<wersja>-instalator.exe`
  (instalator, z niego aktualizacja) i `SerwisDiag-Go-<wersja>.zip` (wersja przenośna), każdy z sumą `.sha256`.
- **rejestracja** (od 2026-10-27) – okno „Rejestracja” (dane, zgoda, informacja RODO, wysyłanie w tle), `-Register`
  na końcu instalacji, ponowne wysyłanie w tle (okno i monitor, co najmniej 30 min po nieudanej próbie). Plik
  stanu `%LOCALAPPDATA%\SerwisDiag\rejestracja.json` i blokada wysyłania są **wspólne z wersją PowerShell** – jedna
  rejestracja użytkownika Windows dla obu wersji. Jak w wersji PowerShell: bez adresu formularza
  (`diag.Registration.FormURL`) rejestracja jest wyłączona i niewidoczna.
- **instalator** (od 2026-10-27) – Inno Setup 7 (`tools\installer\SerwisDiagGo.iss`, budowanie:
  `tools\Build-Release.ps1`, razem z wersją przenośną ZIP): dla wszystkich użytkowników (Program Files, UAC) albo „tylko dla mnie” (bez UAC),
  skróty, monitor w tle jako zadanie wyboru, `instalacja.ini` (raporty w Dokumentach, tryb do aktualizacji),
  ustawienia serwisu zapisywalne dla użytkowników, pole „Zarejestruj program” (gdy jest adres formularza),
  odinstalowanie bez raportów klientów. Własny identyfikator i folder (`SerwisDiag Go`) – instaluje się obok wersji
  PowerShell. Monitor w instalacji dla wszystkich: **zadanie Harmonogramu** `SerwisDiag\Monitor Go` (każdy
  użytkownik przy logowaniu, najwyższe dostępne uprawnienia) – jak w wersji PowerShell.

Wszystkie funkcje wersji PowerShell 1.5.0 są przeniesione.

## Różnice względem wersji PowerShell

- **Testy równolegle.** Domyślnie niezależne testy działają jednocześnie (gorutyny). Próbkowanie obciążenia
  (`pf.sample`) idzie najpierw i samo, żeby praca innych testów nie zawyżała pomiaru procesora; testy
  obciążeniowe – na końcu, po kolei. `-Sequential` uruchamia wszystko po kolei, jak w wersji PowerShell.
- **Ta sama kolejność przy remisach.** `Sort-Object` w Windows PowerShell 5.1 nie jest stabilny (stary
  quicksort z .NET 4.0) – wersja Go odtwarza ten algorytm (`psSort`), więc np. „najczęstszy rodzaj restartu”
  przy remisie wychodzi taki sam. Wyjątek: listy budowane w PowerShellu z tablic mieszających (`$hash.Values`)
  mają kolejność zależną od skrótów .NET – tam przy remisie wersje mogą wskazać inny element.
- **Liczby po polsku jak w .NET Framework**: twarda spacja jako separator tysięcy i zaokrąglanie „połówka od
  zera” na 15 cyfrach znaczących (2,5 → 3; 1,005 → 1,01) – inaczej niż domyślnie w Go.
- **Mniej zapytań WMI**: procesy, usługi, konta, trasy, porty i plan zasilania z API Windows (szybciej, te same
  dane). Zapytania WMI pobierają tylko potrzebne pola.
- **Bateria**: w wersji PowerShell `Get-CimInstance BatteryStaticData` (wszystkie pola) potrafi zwrócić
  „Generic failure” i program sięga po raport `powercfg`; wersja Go pyta o wybrane pola i dostaje dane z WMI.
  Wartości są te same, brakuje tylko wiersza „Technologia” z raportu powercfg.
- **Wyszukiwanie w Windows Update** w osobnym wątku z limitem 120 s (w PowerShellu: osobny proces `Start-Job`).
- **Kontrola plików programu**: zamiast manifestu (program to jeden plik) raport podaje początek sumy SHA-256
  pliku `serwisdiag.exe`.
- W raporcie JSON jest dodatkowe pole `"engine": "go"`, a w metadanych `GoVersion` zamiast `PSVersion`.
- **Tekst z rejestru kończy się na znaku NUL.** Gdy wartość ma w środku znak NUL (np. `DisplayName` programu
  zapisane przez instalator z dodatkowym zerem), wersja PowerShell przepisuje go do listy programów w kopii
  (bajt zerowy w CSV i HTML), a wersja Go ucina napis na tym znaku.
- **Okno bez WPF**: karty, plakietki i zaokrąglenia rysuje sam program (wygładzane krawędzie liczone w Go),
  pola, listy i przyciski to kontrolki systemowe Windows. Szczegóły i zalecenie wybranego wyniku pojawiają się
  w panelu pod listą (w WPF – rozwinięcie wiersza), a narzędzia serwisowe to kafelki z opisem zamiast podpowiedzi.
- **Monitor Go ma własne nazwy** (blokada `Local\SerwisDiagGo.Monitor`, stan `%LOCALAPPDATA%\SerwisDiag\monitor-go.json`,
  wpis autostartu „SerwisDiag Go Monitor”, zadanie `SerwisDiag\Monitor Go`, stan sprawdzania wersji
  `aktualizacje-go.json`) i podpisuje się „SerwisDiag Go” – działa obok monitora wersji PowerShell i nie miesza
  ich stanu.
- **Kilka kopii programu na jednym komputerze** (np. zainstalowana i przenośna): `-DisableMonitor` (instalator,
  odinstalowanie) usuwa tylko wpisy autostartu wskazujące tę kopię i zatrzymuje tylko jej monitor (każda kopia ma
  własne zdarzenie zatrzymania). Nowe `-StopMonitor` zatrzymuje monitor tej kopii bez zmiany autostartu – instalator
  wywołuje je przed podmianą plików (plik `.exe` działającego monitora jest zablokowany; skrypty wersji PowerShell
  nie). Monitor kończy pracę także na prośbę Menedżera ponownego uruchamiania (np. monitor innego użytkownika).
- **Monitor i okno działają też w kopii przenośnej** (wersja PowerShell włącza monitor tylko w zainstalowanej);
  sprawdzanie nowej wersji – jak w PowerShellu – tylko w zainstalowanej. Program działa bez uprawnień
  administratora (`asInvoker`; w oknie „Uruchom jako administrator”), więc instalator nadaje użytkownikom prawo
  zapisu do `config` (ustawienia serwisu), a po instalacji uruchamia program jako użytkownik, nie administrator.

Przy przenoszeniu wyszedł **błąd w wersji PowerShell**: test `up.dotnet` („Wersje .NET Framework”) nigdy nie
pokazuje wyniku – `[ordered]@{ 533320 = '4.8.1'; ... }` z kluczami liczbowymi indeksuje się po pozycji, więc
`$map[$k]` zwraca `$null`. Wersja Go pokazuje wersję .NET Framework.

Drugi błąd wersji PowerShell: okno „Porównaj przed/po” podpowiada jako raport „przed” najnowszy plik `*.json`
tego komputera – po naprawach jest nim dziennik napraw (`*_naprawy.json`), więc porównanie kończy się komunikatem
„To nie jest raport JSON programu SerwisDiag”. Wersja Go pomija dzienniki napraw przy podpowiadaniu raportu;
w wersji PowerShell poprawione 2026-10-27 w 1.5.0 (jeszcze nieopublikowanej, z testem).

## Budowanie i uruchamianie

Wymagany Go 1.21+ (sprawdzone na 1.27.2). Jedyna zależność: `golang.org/x/sys`.

```
powershell -ExecutionPolicy Bypass -File build.ps1        # serwisdiag.exe, serwisdiag-konsola.exe, sdbench, sdguibench, sdcompare
serwisdiag.exe                                           # okno programu
serwisdiag.exe -Elevate -Ticket 2026-001 -Customer "Jan Kowalski"   # okno z uprawnieniami administratora i danymi zlecenia
serwisdiag.exe -Monitor                                  # monitor przy zegarze
serwisdiag.exe -EnableMonitor                            # autostart monitora przy logowaniu (i uruchomienie)
serwisdiag.exe -DisableMonitor                           # wyłączenie monitora i autostartu (tej kopii programu)
serwisdiag.exe -StopMonitor                              # zatrzymanie monitora tej kopii, autostart zostaje
serwisdiag-konsola.exe -Update                           # wersja zainstalowana: nowa wersja z GitHuba
serwisdiag.exe -Register                                 # okno rejestracji (gdy program ma adres formularza)
powershell -ExecutionPolicy Bypass -File tools\Build-Release.ps1     # dist\SerwisDiag-Go-<wersja>-instalator.exe i .zip (+ .sha256)
serwisdiag-konsola.exe                                   # diagnostyka szybka w konsoli, raporty w .\raporty
serwisdiag-konsola.exe -Level Full -Ticket 2026-001 -Customer "Jan Kowalski" -Pdf
serwisdiag-konsola.exe -Sequential                       # testy po kolei (do porównań z PowerShellem)
serwisdiag-konsola.exe -RepairList                       # diagnostyka i lista tego, co program może naprawić (bez zmian)
serwisdiag-konsola.exe -Elevate -Repair                  # diagnostyka i zalecane naprawy (punkt przywracania, dziennik napraw)
serwisdiag-konsola.exe -Elevate -RepairIds fx.dism,fx.sfc   # wskazane naprawy
serwisdiag-konsola.exe -Elevate -UndoRepair ostatni      # cofnięcie zmian ustawień z najnowszego dziennika napraw
serwisdiag-konsola.exe -CompareWith raporty\PC_20261027-1000.json -WorkDone "Wymiana dysku na SSD"   # diagnostyka i raport z naprawy
serwisdiag-konsola.exe -CompareBefore przed.json -CompareAfter po.json -WorkDone "Czyszczenie, nowa pasta"   # tylko porównanie
serwisdiag-konsola.exe -Elevate -InstallDrivers          # diagnostyka i zalecane sterowniki z Windows Update
serwisdiag-konsola.exe -Elevate -Backup E:\ -BackupInclude drivers,programs,system,wifi   # kopia przed reinstalacją
serwisdiag.exe -SelfTest -ScreenshotPath C:\temp\zrzut   # autotest okna: szybka diagnostyka poza ekranem, zrzuty stron
go test -short ./...                                     # testy jednostkowe
go test ./...                                            # także testy na żywym systemie (WMI, dzienniki, usługi)
```

Działający monitor Go trzyma plik `serwisdiag.exe`, więc `build.ps1` zatrzymuje go na czas budowania i potem
uruchamia ponownie (otwarte okno programu trzeba zamknąć – skrypt to sprawdza).

Autotest monitora jak w wersji PowerShell: `SERWISDIAG_MONITOR_TEST=<prefiks>` (pierwsze sprawdzenie po 2 s, zrzut
okna i ikony, `<prefiks>-wynik.txt`, koniec) i `SERWISDIAG_MONITOR_STATE=<plik>` (osobny plik stanu).

Autotest okna z `SERWISDIAG_SELFTEST_DIALOGS=1` robi też zrzuty okien dialogowych, w tym okna napraw (tylko
wykrywanie – autotest niczego nie naprawia; widok wyniku z wynikami przykładowymi), okna cofania napraw
(`SERWISDIAG_SELFTEST_UNDO=<plik *_naprawy.json>` – dziennik do pokazania), okna porównania przed/po, okna
sterowników (prawdziwe wyszukiwanie w Windows Update, bez instalacji), brakujących sterowników (bez urządzeń
z błędami – przykładowe) i kopii przed reinstalacją (bez tworzenia kopii). Test podłączania obrazu ISO i przygotowania DISM: `SERWISDIAG_TEST_ISO=<plik .iso
z sources\install.wim> go test -run "MountISOLive|DismFromISOLive" ./internal/...`. `SERWISDIAG_SELFTEST_UPDATE=1` –
przykładowy pasek „Dostępna nowa wersja” (bez łączenia z GitHubem). Okno rejestracji: `serwisdiag.exe -Register
-ScreenshotPath <prefiks>` (zrzut pustego okna i z błędami w danych, bez zapisu i wysyłania; zanim program dostanie
adres formularza – z `SERWISDIAG_SELFTEST_REGISTER=1`).

Instalator sprawdzony 2026-10-27 (instalacja „dla mnie” bez monitora do folderu tymczasowego, aktualizacja przy
działającym monitorze tej kopii, odinstalowanie): pliki, `instalacja.ini`, skrót i wpis na liście programów
znikają, monitor tej kopii kończy pracę przed podmianą plików, a monitor i autostart kopii z innego folderu zostają.

Instalacja dla wszystkich użytkowników sprawdzona tego samego dnia (UAC, monitor zaznaczony): Program Files,
`Tryb=wszyscy`, `config` z prawem „Modyfikacja” dla Użytkowników (zapis obok plików programu zablokowany), skrót we
wspólnym menu Start, wpis HKLM, zadanie `SerwisDiag\Monitor Go` (grupa Użytkownicy, najwyższe dostępne uprawnienia,
30 s po logowaniu; zwykły użytkownik je odczyta, uruchomić na żądanie może tylko administrator). Odinstalowanie
usuwa zadanie, pliki, skrót i wpis; wpis Run i monitor kopii deweloperskiej zostają. Ręczne uruchomienie zadania
wykryło dwa błędy (poprawione, z testami):

- `CreateMutex` / `CreateEvent` z `golang.org/x/sys` zwracają `ERROR_ALREADY_EXISTS` **razem z poprawnym uchwytem**,
  gdy obiekt już istnieje – program brał to za błąd. Monitor uruchomiony, gdy działał już inny, kończył się od razu
  kodem 3 zamiast poczekać do 20 s (np. po podmianie pliku programu mogło nie zostać żadnego monitora), a wysyłanie
  rejestracji kończyło się błędem, gdy wspólną blokadę miała otwartą wersja PowerShell. Sprawdzaj uchwyt (`h != 0`),
  nie `err`.
- Blokada (mutex) Windows należy do **wątku systemowego**, a gorutyna Go może zmienić wątek między przejęciem
  a zwolnieniem – `ReleaseMutex` wtedy nie działa, a inne procesy czekają na blokadę do zamknięcia programu. Kod
  trzymający blokadę przypina wątek (`runtime.LockOSThread`; okno i monitor mają go przypiętego przez `initUI`).

Monitor z zadania działa u administratora z pełnymi uprawnieniami, więc aktualizacja zatrzymuje go `-StopMonitor`
uruchomionym z uprawnieniami instalatora (zwykły użytkownik nie ma do tego prawa). `schtasks /Delete` usuwa samo
zadanie – pusty folder `\SerwisDiag` w Harmonogramie zostawał po odinstalowaniu, więc `-DisableMonitor` usuwa go
teraz, gdy nie ma w nim zadań (zadanie wersji PowerShell też jest w tym folderze – wtedy folder zostaje).
Poprawki sprawdzone testami jednostkowymi, bez uprawnień administratora (drugi monitor czeka 21,5 s i kończy się
kodem 0) i w powtórnym teście instalacji dla wszystkich (UAC): monitor uruchomiony z zadania Harmonogramu czeka na
blokadę działającego monitora i kończy się kodem 0 po 20,9 s (wynik zadania 0), a odinstalowanie usuwa zadanie
razem z pustym folderem `\SerwisDiag` – w Harmonogramie nie zostaje nic.

Raporty trafiają tam, gdzie w wersji PowerShell: katalog z `config\settings.json`, potem `raporty\` obok
programu, potem `Dokumenty\SerwisDiag`, na końcu `%TEMP%\SerwisDiag`.

## Nowa wersja (publikacja)

1. Numer wersji w `internal/diag/core.go` (`Version`) i w `winres/winres.json` (`version`, `file_version`,
   `product_version`, `FileVersion`, `ProductVersion`) – `Build-Release.ps1` sprawdza, że się zgadzają, a aktualizacja
   z programu porównuje numer zapisany w pliku instalatora z numerem wydania.
2. `powershell -ExecutionPolicy Bypass -File tools\Build-Release.ps1` – testy, pliki w `dist\`.
3. Commit i `git push`, potem wydanie z czterema plikami (opis zwykłym tekstem – program pokazuje go w „Co nowego”):

```
gh release create v<X> dist\SerwisDiag-Go-<X>-instalator.exe dist\SerwisDiag-Go-<X>-instalator.exe.sha256 dist\SerwisDiag-Go-<X>.zip dist\SerwisDiag-Go-<X>.zip.sha256 --title "SerwisDiag Go <X>" --notes-file opis.txt
```

4. Sprawdzenie wydania (z internetem): `go test -run TestLatestReleaseLive -v ./internal/diag` – najnowsze wydanie
   ma wszystkie pliki, a instalator pobiera się z poprawną sumą SHA-256 i numerem wersji.

Wersje zainstalowane sprawdzają najnowsze wydanie raz na dobę (`releases/latest` – wydania próbne oznaczaj jako
„pre-release”, wtedy program ich nie widzi). Sprawdzone 2026-10-27 na wydaniu v1.5.0: wersja 1.4.9 (zbudowana
z kopii kodu ze zmienionym numerem) zainstalowana „dla mnie” po `-Update` pobrała z GitHuba instalator 1.5.0,
sprawdziła sumę i numer wersji i w 8 s zaktualizowała się w tym samym trybie; potem „Masz najnowszą wersję (1.5.0)”.

## Porównanie wyników i wydajności

- `sdcompare.exe raport-ps.json raport-go.json` – wyniki obecne tylko w jednym raporcie, różnice statusu
  i treści, liczba wierszy tabel, metadane.
- `sdbench.exe -runs 3 -level Quick [-offline] -out <folder>` – uruchamia obie wersje na zmianę, każdą
  w osobnym obiekcie zadania (liczy też procesy potomne), i zapisuje tabelę: czas, czas procesora, szczyt
  pamięci, liczba procesów, czas procesora usługi WMI. Raporty z pomiarów są usuwane (mają dane komputera),
  chyba że podasz `-keep`.
- `sdguibench.exe -mode start|selftest|monitor -runs 3 -work <folder>` – okno i monitor obu wersji: czas do pokazania
  okna i jego pamięć, autotest okna z szybką diagnostyką, jedno sprawdzenie monitora (`-state <plik>`: stan
  z poprzedniego sprawdzenia, żeby wersja PowerShell nie pokazywała powiadomienia o znanych problemach).
- `tools\porownaj-monitory.ps1 [-Csv]` – monitory obu wersji działające obok siebie w codziennej pracy (oba
  startują przy logowaniu): pamięć prywatna i robocza (obecna i szczytowa), czas procesora od startu i na godzinę,
  wątki, uchwyty, ostatnie sprawdzenie i liczba problemów. `-Csv` dopisuje pomiar do `bench\monitory.csv`; przy
  następnym uruchomieniu skrypt pokazuje czas procesora obu monitorów od tamtego pomiaru (ten sam okres – uczciwe
  porównanie także wtedy, gdy monitory wystartowały o różnych porach).

Wyniki pomiarów: [WYNIKI.md](WYNIKI.md).

## Struktura

```
main.go                   parametry jak w SerwisDiag.ps1: okno, monitor albo tryb konsolowy
internal/win/             warstwa systemowa: com.go, wmi.go, oleauto.go, evt.go, reg.go, native.go, perf.go,
                          svc.go, net.go, netapi.go, fileinfo.go, tasks.go, dism.go, bench.go, wmimethod.go
                          (metody WMI), restore.go (punkt przywracania), process.go (deinstalatory),
                          vdisk.go (podłączanie obrazu ISO)
internal/diag/            silnik: core.go (rejestr testów, skanowanie po kolei/równolegle), providers.go,
                          c10_system.go … c99_tests.go (testy), report.go, anon.go, format.go, uiapi.go, data/*.json,
                          fixes.go + fixes_catalog.go (naprawy, dziennik napraw), undo.go (cofanie), wudrivers.go,
                          compare.go (porównanie przed/po), image.go (DISM z płyty/ISO), drivers_ui.go,
                          backup.go (kopia przed reinstalacją), update.go (nowa wersja z GitHuba),
                          registration.go (rejestracja)
internal/ui/              okno i monitor: w32.go (API Windows), gfx.go (płótno, czcionki, zawijanie tekstu),
                          app.go (pętla komunikatów), controls.go, doc.go (przewijana treść rysowana), listview.go,
                          mainwin.go, pages.go, scan.go, dialogs.go, tools.go, icon.go, monitor.go,
                          fixes.go (okna napraw i cofania napraw), compare.go (okno porównania przed/po),
                          drivers.go (okna sterowników, strona producenta, DISM z płyty/ISO), backup.go (okno kopii),
                          update.go (pasek nowej wersji, aktualizacja), register.go (okno rejestracji)
cmd/sdcompare/            porównanie dwóch raportów JSON
cmd/sdbench/              pomiar wydajności trybu konsolowego obu wersji
cmd/sdguibench/           pomiar okna i monitora obu wersji (start okna, autotest z diagnostyką, sprawdzenie monitora)
tools/export-data.ps1     przeniesienie bazy wiedzy z wersji PowerShell (po zmianie data\*.psd1)
tools/porownaj-monitory.ps1  porównanie monitorów obu wersji działających obok siebie (pamięć, czas procesora)
tools/installer/SerwisDiagGo.iss  skrypt instalatora (Inno Setup 7)
tools/Build-Release.ps1      pliki wydania: instalator i wersja przenośna ZIP z sumami SHA-256 (budowanie w folderze tymczasowym)
LICENSE                   licencja MIT (ten sam tekst co w wersji PowerShell)
winres/                   ikona, manifest i informacje o wersji (go-winres)
```
