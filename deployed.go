package main

import (
	"context"
	"errors"
	"time"

	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// Deployed mods (experimental): the mods Vortex deployed into a game's own
// folder, synced so another PC can play with them right away.

var errDeployedLater = errors.New("syncing deployed mods isn't available yet")

func (a *App) addDeployedSource(f mods.Found) error { return errDeployedLater }

func (a *App) joinDeployed(ctx context.Context, c *syncthing.Client, v meta.Avail) error {
	return errDeployedLater
}

func deployedSize(f mods.Found) (int64, int, time.Time) { return mods.Measure(f.Path) }

// deployedScope are the .stignore lines that limit a deployed-mods folder
// to the mod files.
func deployedScope(id, path string) []string { return nil }

// holdAllDeployed pauses every deployed-mods folder and holds its updates.
func (a *App) holdAllDeployed(why string) {}

// modView fills in a mod folder's update state.
func (a *App) modView(v *FolderView) {
	st := store.LoadState().ModSync[v.ID]
	v.ModPhase, v.ModHeld = st.Phase, st.Held
}
