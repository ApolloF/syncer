package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/update"
)

// UpdateInfo is a newer Syncer release.
type UpdateInfo struct {
	Latest string `json:"latest"`
	URL    string `json:"url"`
}

const updateEvery = 24 * time.Hour

// updateLoop looks for a new release a minute after start, then daily.
func (a *App) updateLoop(ctx context.Context) {
	if version == "dev" {
		return
	}
	sleep(ctx, time.Minute)
	for ctx.Err() == nil {
		if u := store.LoadState().Update; u == nil || time.Since(u.Checked) > updateEvery {
			if _, err := a.checkUpdate(ctx); err != nil {
				logx.Printf("update check: %v", err)
			}
		}
		sleep(ctx, time.Hour)
	}
}

// checkUpdate asks GitHub for the newest release and remembers it.
func (a *App) checkUpdate(ctx context.Context) (*UpdateInfo, error) {
	if version == "dev" {
		return nil, errors.New("this is a development build")
	}
	if store.LoadSettings().NoUpdateCheck {
		return nil, nil
	}
	r, err := update.Latest(ctx)
	if err != nil {
		return nil, err
	}
	store.UpdateState(func(st *store.State) {
		st.Update = &store.Update{Checked: time.Now(), Latest: r.Tag, URL: r.URL}
	})
	u := availableUpdate()
	if u != nil {
		logx.Printf("Syncer %s is available (this is %s)", u.Latest, version)
		a.refreshTray()
		runtime.EventsEmit(a.ctx, "changed")
	}
	return u, nil
}

// CheckForUpdate checks now (Settings); nil means this is the newest version.
func (a *App) CheckForUpdate() (*UpdateInfo, error) {
	ctx, cancel := a.callCtx()
	defer cancel()
	return a.checkUpdate(ctx)
}

// availableUpdate is the newer release found by the last check, if any.
func availableUpdate() *UpdateInfo {
	u := store.LoadState().Update
	if u == nil || store.LoadSettings().NoUpdateCheck || !update.Newer(u.Latest, version) ||
		!strings.HasPrefix(u.URL, update.ReleasesURL) {
		return nil
	}
	return &UpdateInfo{Latest: u.Latest, URL: u.URL}
}

// OpenUpdate opens the page of the newer release.
func (a *App) OpenUpdate() {
	if u := availableUpdate(); u != nil {
		runtime.BrowserOpenURL(a.ctx, u.URL)
	}
}
