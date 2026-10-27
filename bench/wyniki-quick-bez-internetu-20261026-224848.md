## Scenariusz: Quick bez internetu (3 pomiary każdej wersji, 2026-10-27 02:51)

| Wersja | Czas całkowity [s] | Czas procesora [s] | Szczyt pamięci procesu [MB] | Szczyt pamięci zadania [MB] | Procesy | CPU usługi WMI [s] | Wynik |
|---|---|---|---|---|---|---|---|
| PowerShell | 37,1 (28,2–14354,3) | 10,6 (10,3–12,6) | 324 (312–332) | 290 (277–298) | 10 | 7,6 (4,2–11,1) | kryt. 0, ostrz. 4, info 22, OK 47, pomin. 8 |
| Go (równolegle) | 12,4 (9,2–13,2) | 1,0 (0,9–1,3) | 40 | 104 (104–106) | 4 | 7,3 (3,2–8,3) | kryt. 0, ostrz. 4, info 23, OK 47, pomin. 8 |
| Go (po kolei) | 18,3 (14,4–18,6) | 0,9 (0,8–1,1) | 38 | 101 | 4 | 4,8 (2,7–5,2) | kryt. 0, ostrz. 4, info 23, OK 47, pomin. 8 |

W nawiasach: najkrótszy i najdłuższy pomiar; poza nawiasem mediana.
