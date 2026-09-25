// Package tasks manages Windows Task Scheduler entries via schtasks.exe.
package tasks

import (
	"encoding/binary"
	"fmt"
	"html"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const (
	BackupTask       = `\Syncer-Background`
	SyncthingTask    = `\Syncthing-AutoStart`
	LegacyBackupTask = `\GameSave-GDrive-Backup`
)

var (
	schtasksOnce sync.Once
	schtasksPath string
)

// schtasksExe resolves the absolute path to schtasks.exe under the system
// directory, so a malicious schtasks.exe earlier on PATH can't be run
// instead. Falls back to the bare name only if the system directory can't
// be determined.
func schtasksExe() string {
	schtasksOnce.Do(func() {
		dir, err := windows.GetSystemDirectory()
		if err != nil {
			schtasksPath = "schtasks.exe"
			return
		}
		schtasksPath = filepath.Join(dir, "schtasks.exe")
	})
	return schtasksPath
}

func run(args ...string) (string, error) {
	cmd := exec.Command(schtasksExe(), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Exists reports whether a task is registered.
func Exists(name string) bool {
	_, err := run("/query", "/tn", name)
	return err == nil
}

// Delete removes a task if present.
func Delete(name string) error {
	if !Exists(name) {
		return nil
	}
	out, err := run("/delete", "/tn", name, "/f")
	if err != nil {
		return fmt.Errorf("delete task %s: %s", name, out)
	}
	return nil
}

// Run starts a task immediately.
func Run(name string) error {
	out, err := run("/run", "/tn", name)
	if err != nil {
		return fmt.Errorf("run task %s: %s", name, out)
	}
	return nil
}

// Spec describes a per-user task.
type Spec struct {
	Name        string
	Description string
	Exe         string
	Args        string
	AtLogon     bool
	LogonDelay  time.Duration
	RepeatEvery time.Duration // 0 = no repetition
	TimeLimit   time.Duration // 0 = unlimited
}

// Register creates or replaces a task.
func Register(s Spec) error {
	xml := s.xml()
	f, err := os.CreateTemp("", "syncer-task-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	// schtasks requires UTF-16 for XML with an encoding declaration.
	u := utf16.Encode([]rune(xml))
	buf := make([]byte, 2+2*len(u))
	buf[0], buf[1] = 0xFF, 0xFE
	for i, c := range u {
		binary.LittleEndian.PutUint16(buf[2+2*i:], c)
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return err
	}
	f.Close()
	out, err := run("/create", "/tn", s.Name, "/xml", f.Name(), "/f")
	if err != nil {
		return fmt.Errorf("register task %s: %s", s.Name, out)
	}
	return nil
}

func dur(d time.Duration) string {
	if d <= 0 {
		return "PT0S"
	}
	m := int(d.Minutes())
	if m%60 == 0 {
		return fmt.Sprintf("PT%dH", m/60)
	}
	return fmt.Sprintf("PT%dM", m)
}

func (s Spec) xml() string {
	uid := ""
	if u, err := user.Current(); err == nil {
		uid = html.EscapeString(u.Username)
	}
	var trig strings.Builder
	if s.AtLogon {
		fmt.Fprintf(&trig, `<LogonTrigger><Enabled>true</Enabled><UserId>%s</UserId><Delay>%s</Delay></LogonTrigger>`,
			uid, dur(s.LogonDelay))
	}
	if s.RepeatEvery > 0 {
		start := time.Now().Add(5 * time.Minute).Format("2006-01-02T15:04:05")
		fmt.Fprintf(&trig, `<TimeTrigger><Enabled>true</Enabled><StartBoundary>%s</StartBoundary>`+
			`<Repetition><Interval>%s</Interval><StopAtDurationEnd>false</StopAtDurationEnd></Repetition></TimeTrigger>`,
			start, dur(s.RepeatEvery))
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>` + html.EscapeString(s.Description) + `</Description></RegistrationInfo>
  <Triggers>` + trig.String() + `</Triggers>
  <Principals><Principal id="Author"><UserId>` + uid + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <ExecutionTimeLimit>` + dur(s.TimeLimit) + `</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + html.EscapeString(s.Exe) + `</Command><Arguments>` + html.EscapeString(s.Args) + `</Arguments><WorkingDirectory>` + html.EscapeString(filepath.Dir(s.Exe)) + `</WorkingDirectory></Exec></Actions>
</Task>`
}
