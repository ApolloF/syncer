package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/store"
)

// Windows notifications about problems, while Syncer runs (window or tray).
// The background task records its results in state.json; this reads them
// once a minute, so its failures are reported too.

const staleBackup = 3 * 24 * time.Hour

type problem struct{ key, title, body string }

func (a *App) notifyLoop(ctx context.Context) {
	if err := runtime.InitializeNotifications(a.ctx); err != nil {
		logx.Printf("notifications: %v", err)
		return
	}
	runtime.OnNotificationResponse(a.ctx, func(runtime.NotificationResult) { a.showWindow() })
	sleep(ctx, 2*time.Minute) // let startup settle
	for ctx.Err() == nil {
		a.notifyProblems()
		sleep(ctx, time.Minute)
	}
}

// notifyProblems sends a notification for every new problem, once.
func (a *App) notifyProblems() {
	s := store.LoadSettings()
	if !s.Notify {
		return
	}
	st := store.LoadState()
	var conflicts map[string]int
	var labels map[string]string
	if fs, err := syncedFolders(); err == nil && !s.SyncDisabled {
		conflicts = a.conflictCounts(fs)
		labels = map[string]string{}
		for _, f := range fs {
			labels[f.ID] = cmpOr(f.Label, f.ID)
		}
	}
	ps := problems(s, st, conflicts, labels, availableUpdate(), time.Now())
	if len(ps) == 0 && len(st.Notified) == 0 {
		return
	}
	sent := map[string]bool{}
	for _, p := range ps {
		if _, done := st.Notified[p.key]; done {
			continue
		}
		if err := runtime.SendNotification(a.ctx, runtime.NotificationOptions{ID: p.key, Title: p.title, Body: p.body}); err != nil {
			logx.Printf("notification: %v", err)
			continue
		}
		sent[p.key] = true
	}
	store.UpdateState(func(st *store.State) {
		if st.Notified == nil {
			st.Notified = map[string]time.Time{}
		}
		for k := range sent {
			st.Notified[k] = time.Now()
		}
		for k, t := range st.Notified {
			// A settled conflict may be reported again when it comes back.
			if id, ok := strings.CutPrefix(k, "conflict:"); ok && conflicts != nil && conflicts[id] == 0 {
				delete(st.Notified, k)
			} else if time.Since(t) > 60*24*time.Hour {
				delete(st.Notified, k)
			}
		}
	})
}

// problems lists what's worth a notification right now. Each key identifies
// the problem, so it's reported once.
func problems(s store.Settings, st store.State, conflicts map[string]int, labels map[string]string,
	upd *UpdateInfo, now time.Time) []problem {
	var ps []problem
	lb := st.LastBackup
	if s.BackupEnabled && lb != nil && !lb.OK && !lb.Finished.IsZero() && now.Sub(lb.Finished) < 24*time.Hour {
		body := "Open Syncer to see what went wrong."
		if len(lb.Errors) > 0 {
			body = lb.Errors[0]
			if len(lb.Errors) > 1 {
				body += fmt.Sprintf(" (and %d more)", len(lb.Errors)-1)
			}
		}
		ps = append(ps, problem{"backup-failed:" + lb.Finished.UTC().Format(time.RFC3339), "Game save backup had problems", body})
	}
	last := st.LastSuccess
	if last.IsZero() && lb != nil && lb.OK {
		last = lb.Finished
	}
	if s.BackupEnabled && !s.Paused() && !last.IsZero() && now.Sub(last) > staleBackup {
		days := int(now.Sub(last).Hours() / 24)
		ps = append(ps, problem{"stale:" + now.Format("2006-01-02"), "No game save backup in " + fmt.Sprint(days) + " days",
			"The last successful backup was on " + last.Format("Mon 2 Jan") + ". Open Syncer to check Google Drive."})
	}
	for id, n := range conflicts {
		if n > 0 {
			ps = append(ps, problem{"conflict:" + id, labels[id] + " has two versions of a save",
				"Two PCs changed the same save. Open Syncer and pick which one to keep."})
		}
	}
	if upd != nil {
		ps = append(ps, problem{"update:" + upd.Latest, "Syncer " + upd.Latest + " is available",
			"Open Syncer to download it."})
	}
	return ps
}
