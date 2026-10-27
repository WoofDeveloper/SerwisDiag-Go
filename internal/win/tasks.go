package win

import "strings"

// Harmonogram zadań przez obiekt Schedule.Service (to samo, co Get-ScheduledTask).

// ScheduledTask - zadanie harmonogramu.
type ScheduledTask struct {
	Path     string // folder zadania z ukośnikiem na końcu, np. "\Mozilla\" (jak TaskPath)
	Name     string
	State    string // Unknown, Disabled, Queued, Ready, Running
	Exec     string // pierwsza akcja typu "uruchom program"
	Args     string
	Triggers []string // rodzaje wyzwalaczy, np. Logon, Daily
}

var (
	taskStates   = map[int64]string{0: "Unknown", 1: "Disabled", 2: "Queued", 3: "Ready", 4: "Running"}
	triggerNames = map[int64]string{0: "Event", 1: "Time", 2: "Daily", 3: "Weekly", 4: "Monthly", 5: "MonthlyDOW", 6: "Idle", 7: "Registration", 8: "Boot", 9: "Logon", 11: "SessionStateChange"}
)

// ScheduledTasks zwraca zadania (także ukryte); skip: foldery pomijane w całości, np. \Microsoft.
// Wymaga wątku z COM (WithCOM).
func ScheduledTasks(skip func(folder string) bool) ([]ScheduledTask, error) {
	svc, err := CreateObject("Schedule.Service")
	if err != nil {
		return nil, err
	}
	defer svc.Release()
	if _, err := svc.Get("Connect"); err != nil {
		return nil, err
	}
	root, err := svc.Obj("GetFolder", `\`)
	if err != nil {
		return nil, err
	}
	defer root.Release()
	var out []ScheduledTask
	var walk func(f *Dispatch)
	walk = func(f *Dispatch) {
		folderPath := f.Str("Path")
		taskPath := strings.TrimSuffix(folderPath, `\`) + `\`
		if tasks, err := f.Obj("GetTasks", 1); err == nil { // TASK_ENUM_HIDDEN
			n := int(tasks.Int("Count"))
			for i := 1; i <= n; i++ {
				t, err := tasks.Obj("Item", i)
				if err != nil {
					continue
				}
				st := ScheduledTask{Path: taskPath, Name: t.Str("Name"), State: taskStates[t.Int("State")]}
				if def, err := t.Obj("Definition"); err == nil {
					if acts, err := def.Obj("Actions"); err == nil {
						for j := 1; j <= int(acts.Int("Count")); j++ {
							if a, err := acts.Obj("Item", j); err == nil {
								if a.Int("Type") == 0 && st.Exec == "" {
									st.Exec, st.Args = a.Str("Path"), a.Str("Arguments")
								}
								a.Release()
							}
						}
						acts.Release()
					}
					if trig, err := def.Obj("Triggers"); err == nil {
						for j := 1; j <= int(trig.Int("Count")); j++ {
							if tr, err := trig.Obj("Item", j); err == nil {
								name := triggerNames[tr.Int("Type")]
								dup := false
								for _, x := range st.Triggers {
									dup = dup || x == name
								}
								if !dup {
									st.Triggers = append(st.Triggers, name)
								}
								tr.Release()
							}
						}
						trig.Release()
					}
					def.Release()
				}
				out = append(out, st)
				t.Release()
			}
			tasks.Release()
		}
		if subs, err := f.Obj("GetFolders", 0); err == nil {
			n := int(subs.Int("Count"))
			for i := 1; i <= n; i++ {
				sf, err := subs.Obj("Item", i)
				if err != nil {
					continue
				}
				if skip == nil || !skip(sf.Str("Path")) {
					walk(sf)
				}
				sf.Release()
			}
			subs.Release()
		}
	}
	walk(root)
	return out, nil
}

// DeleteEmptyTaskFolder usuwa folder Harmonogramu zadań (np. `\SerwisDiag`) z katalogu głównego, gdy nie ma w nim
// zadań (także ukrytych) ani podfolderów - schtasks /Delete usuwa samo zadanie, a pusty folder zostawałby po
// odinstalowaniu. Wynik: true - folder usunięty. Folder utworzony przez administratora usunie tylko administrator.
// Wymaga wątku z COM (WithCOM).
func DeleteEmptyTaskFolder(path string) (bool, error) {
	name := strings.Trim(path, `\`)
	if name == "" || strings.Contains(name, `\`) {
		return false, nil
	}
	svc, err := CreateObject("Schedule.Service")
	if err != nil {
		return false, err
	}
	defer svc.Release()
	if _, err := svc.Get("Connect"); err != nil {
		return false, err
	}
	f, err := svc.Obj("GetFolder", `\`+name)
	if err != nil {
		return false, nil // nie ma takiego folderu
	}
	count := func(method string, flags int) int64 {
		c, err := f.Obj(method, flags)
		if err != nil {
			return -1
		}
		defer c.Release()
		return c.Int("Count")
	}
	tasks, subs := count("GetTasks", 1), count("GetFolders", 0) // 1 - TASK_ENUM_HIDDEN
	f.Release()
	if tasks != 0 || subs != 0 {
		return false, nil
	}
	root, err := svc.Obj("GetFolder", `\`)
	if err != nil {
		return false, err
	}
	defer root.Release()
	if _, err := root.Get("DeleteFolder", name, 0); err != nil {
		return false, err
	}
	return true, nil
}
