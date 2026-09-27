package main

import "testing"

func TestSessionTracker(t *testing.T) {
	var tr sessionTracker
	isGame := func(p string) bool { return p == `D:\Games\Elden Ring\eldenring.exe` }
	procs := []string{}
	running := func() []string { return procs }
	step := func(fg string) (bool, bool) { return tr.step(fg, isGame, running) }

	if s, e := step(`C:\Windows\explorer.exe`); s || e || tr.active() {
		t.Fatal("no game in front: no session")
	}
	procs = []string{`d:\games\elden ring\eldenring.exe`}
	if s, _ := step(`D:\Games\Elden Ring\eldenring.exe`); !s || !tr.active() {
		t.Fatal("game in front: session starts")
	}
	if s, e := step(`C:\Program Files\Discord\Discord.exe`); s || e || !tr.active() {
		t.Fatal("alt-tab keeps the session")
	}
	procs = nil
	if _, e := step(`C:\Windows\explorer.exe`); !e || tr.active() {
		t.Fatal("game exited: session ends")
	}
	// A game that's in front but already gone by the process list.
	if s, e := step(`D:\Games\Elden Ring\eldenring.exe`); s || e {
		t.Error("a game that isn't running doesn't start a session")
	}
}

func TestPullVerdict(t *testing.T) {
	ok := pullCheck{Auto: true, Ready: true}
	for _, tt := range []struct {
		name         string
		c            pullCheck
		auto, canGet bool
	}{
		{"all clear", ok, true, true},
		{"the other PC is online", with(ok, func(c *pullCheck) { c.Online = true }), false, false},
		{"Syncthing is fetching", with(ok, func(c *pullCheck) { c.Incoming = true }), false, false},
		{"backup not arrived", with(ok, func(c *pullCheck) { c.Ready = false }), false, false},
		{"playing", with(ok, func(c *pullCheck) { c.Playing = true }), false, false},
		{"conflicts", with(ok, func(c *pullCheck) { c.Conflicts = 2 }), false, false},
		{"changed here", with(ok, func(c *pullCheck) { c.Changed = true }), false, true},
		{"paused", with(ok, func(c *pullCheck) { c.Paused = true }), false, true},
		{"switched off", with(ok, func(c *pullCheck) { c.Auto = false }), false, true},
		{"Syncthing not answering", with(ok, func(c *pullCheck) { c.Unknown = true }), false, true},
	} {
		auto, canGet, why := pullVerdict(tt.c)
		if auto != tt.auto || canGet != tt.canGet || (!auto && why == "") {
			t.Errorf("%s: auto=%v canGet=%v why=%q", tt.name, auto, canGet, why)
		}
	}
}

func with(c pullCheck, f func(*pullCheck)) pullCheck {
	f(&c)
	return c
}
