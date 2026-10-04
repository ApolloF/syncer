package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Tests don't ask the network for the time: the clock is right unless a test
// says otherwise.
func TestMain(m *testing.M) {
	netTime = func(context.Context) (time.Time, error) { return time.Now(), nil }
	os.Exit(m.Run())
}

func setNetTime(t *testing.T, f func(context.Context) (time.Time, error)) {
	t.Helper()
	old := netTime
	netTime = f
	t.Cleanup(func() { netTime = old })
}

func TestClockErr(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		net  time.Time
		err  error
		ok   bool
	}{
		{"right", now.Add(-time.Minute), nil, true},
		{"a little off", now.Add(clockSlack - time.Minute), nil, true},
		{"ahead", now.AddDate(-1, 0, 0), nil, false},
		{"behind", now.Add(2 * clockSlack), nil, false},
		{"offline", time.Time{}, errors.New("no network"), false},
	}
	for _, c := range cases {
		setNetTime(t, func(context.Context) (time.Time, error) { return c.net, c.err })
		if err := clockErr(context.Background(), now); (err == nil) != c.ok {
			t.Errorf("%s: clockErr = %v", c.name, err)
		}
	}
}

// A PC whose clock jumped ahead backs up as always but leaves the history be.
func TestRunDoesNotPruneWithWrongClock(t *testing.T) {
	src, target := t.TempDir(), t.TempDir()
	id := "clock-" + time.Now().Format("150405.000000")
	t.Cleanup(func() { os.Remove(indexPath(id)) })
	old := time.Now().AddDate(0, 0, -40)
	write(t, filepath.Join(target, VersionsDir, id, old.Format(stampFmt), "a.sav"), "old")
	madeAt(t, filepath.Join(target, VersionsDir, id, old.Format(stampFmt)), old)
	write(t, filepath.Join(src, "a.sav"), "a")
	run := func() string {
		t.Helper()
		res, err := Run(context.Background(), []Folder{{ID: id, Label: "Game", Path: src}}, Options{Target: target, KeepDays: 30})
		if err != nil || !res.OK {
			t.Fatalf("run: %v %v", err, res)
		}
		return res.NotPruned
	}

	setNetTime(t, func(context.Context) (time.Time, error) { return time.Now().AddDate(-1, 0, 0), nil })
	if run() == "" {
		t.Error("skipped pruning not reported")
	}
	if read(t, filepath.Join(target, id, "a.sav")) != "a" {
		t.Fatal("not backed up")
	}
	if len(Points(target, id)) != 1 {
		t.Fatal("pruned by a clock a year off")
	}

	setNetTime(t, func(context.Context) (time.Time, error) { return time.Time{}, errors.New("offline") })
	run()
	if len(Points(target, id)) != 1 {
		t.Fatal("pruned without checking the clock")
	}

	setNetTime(t, func(context.Context) (time.Time, error) { return time.Now(), nil })
	if why := run(); why != "" {
		t.Errorf("pruning reported skipped: %s", why)
	}
	if len(Points(target, id)) != 0 {
		t.Fatal("expired point kept with the clock right")
	}
}

func TestClockErrSaysWhichWay(t *testing.T) {
	now := time.Now()
	setNetTime(t, func(context.Context) (time.Time, error) { return now.AddDate(0, 0, -365), nil })
	if err := clockErr(context.Background(), now); err == nil || err.Error() != "the PC's clock is 365 days ahead" {
		t.Errorf("ahead: %v", err)
	}
	setNetTime(t, func(context.Context) (time.Time, error) { return now.Add(10 * time.Hour), nil })
	if err := clockErr(context.Background(), now); err == nil || err.Error() != "the PC's clock is 10 hours behind" {
		t.Errorf("behind: %v", err)
	}
}
