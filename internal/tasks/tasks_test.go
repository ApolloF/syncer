package tasks

import (
	"strings"
	"testing"
	"time"
)

func TestXMLOneOff(t *testing.T) {
	at := time.Date(2026, 9, 26, 6, 0, 0, 0, time.Local)
	x := Spec{Name: ResumeTask, Exe: `C:\Apps\Syncer.exe`, Args: "--resume", At: at}.xml()
	if !strings.Contains(x, `<TimeTrigger><Enabled>true</Enabled><StartBoundary>2026-09-26T06:00:00</StartBoundary></TimeTrigger>`) {
		t.Errorf("one-off trigger missing:\n%s", x)
	}
	if strings.Contains(x, "<Repetition>") || strings.Contains(x, "<LogonTrigger>") {
		t.Errorf("unexpected extra triggers:\n%s", x)
	}
	if !strings.Contains(x, `<Arguments>--resume</Arguments>`) {
		t.Errorf("arguments missing:\n%s", x)
	}
	if x := (Spec{Name: BackupTask, AtLogon: true, RepeatEvery: 3 * time.Hour}).xml(); strings.Count(x, "<TimeTrigger>") != 1 {
		t.Errorf("repeating task should have one time trigger:\n%s", x)
	}
}
