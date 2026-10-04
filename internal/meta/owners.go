package meta

import (
	"cmp"
	"context"
	"os"
	"sort"
	"strings"

	"github.com/ApolloF/syncer/internal/accounts"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// Syncthing on a PC may sync more than Syncer's saves: a notes vault with a
// server, say. Syncer works only with the PCs that run Syncer and with its
// own folders, and leaves everything else in Syncthing alone. Earlier
// Syncers took every linked device for a Syncer PC and every folder for
// their own; Reconcile untangles that.

// OwnDevices keeps this PC (me) and the linked PCs known to run Syncer.
func OwnDevices(me string, ds []syncthing.Device) []syncthing.Device {
	pcs := knownPCs()
	var out []syncthing.Device
	for _, d := range ds {
		if d.DeviceID == me || pcs[d.DeviceID] {
			out = append(out, d)
		}
	}
	return out
}

// OwnFolders leaves out the folders found to be something else's.
func OwnFolders(fs []syncthing.Folder) []syncthing.Folder {
	other := store.LoadState().OtherFolders
	if len(other) == 0 {
		return fs
	}
	var out []syncthing.Folder
	for _, f := range fs {
		if !other[f.ID] {
			out = append(out, f)
		}
	}
	return out
}

// knownPCs are the devices recorded as running Syncer, and those with a
// folder list in the shared metadata.
func knownPCs() map[string]bool {
	m := map[string]bool{}
	for id := range store.LoadState().SyncerPCs {
		m[id] = true
	}
	es, _ := os.ReadDir(Dir())
	for _, e := range es {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok && e.Type().IsRegular() && paths.ValidID(id) {
			m[id] = true
		}
	}
	return m
}

// MarkSyncerPC records that device runs Syncer (it was paired in Syncer).
func MarkSyncerPC(id string) {
	store.UpdateState(func(st *store.State) {
		if st.SyncerPCs == nil {
			st.SyncerPCs = map[string]bool{}
		}
		st.SyncerPCs[id] = true
	})
}

// ForgetSyncerPC drops device from the PCs known to run Syncer.
func ForgetSyncerPC(id string) {
	store.UpdateState(func(st *store.State) { delete(st.SyncerPCs, id) })
}

// MarkOwnFolder records that Syncer added folder id.
func MarkOwnFolder(id string) {
	store.UpdateState(func(st *store.State) {
		if st.SyncerFolders == nil {
			st.SyncerFolders = map[string]bool{}
		}
		st.SyncerFolders[id] = true
		delete(st.OtherFolders, id)
	})
}

// syncerPCs returns the linked devices that run Syncer (me left out), and
// records the ones newly found: a device that offers the shared metadata
// folder, or syncs it, runs Syncer.
func syncerPCs(ctx context.Context, c *syncthing.Client, me string, ds []syncthing.Device) map[string]bool {
	known := knownPCs()
	pending, _ := c.PendingFolders(ctx)
	pcs := map[string]bool{}
	var found []string
	for _, d := range ds {
		id := d.DeviceID
		if id == me {
			continue
		}
		if !known[id] {
			_, offers := pending[FolderID].OfferedBy[id]
			if !offers {
				comp, err := c.FolderCompletion(ctx, FolderID, id)
				if err != nil || !comp.Accepted() {
					continue
				}
			}
			found = append(found, id)
		}
		pcs[id] = true
	}
	if len(found) > 0 {
		store.UpdateState(func(st *store.State) {
			if st.SyncerPCs == nil {
				st.SyncerPCs = map[string]bool{}
			}
			for _, id := range found {
				st.SyncerPCs[id] = true
				logx.Printf("meta: %s runs Syncer", id[:min(7, len(id))])
			}
		})
	}
	return pcs
}

// Whose a folder is.
const (
	ownerSyncer  = "syncer"
	ownerOther   = "other"
	ownerUnknown = "" // not decided yet
)

// whose decides whether folder f is Syncer's. A folder another Syncer PC
// syncs is; one that a device without Syncer accepted (and no Syncer PC)
// belongs to something else. Without either, a folder another PC publishes
// is Syncer's; one shared with a device of its own that isn't connected waits
// for it; anything else (shared with no one else, or only offered) is
// Syncer's. strong: Syncer's own settings name it. state returns a device's
// remote state of the folder.
func whose(f syncthing.Folder, me string, pcs, direct map[string]bool, strong, published bool,
	state func(folder, device string) string) string {
	if strong {
		return ownerSyncer
	}
	foreignAccepted, foreignWaiting := false, false
	for _, fd := range f.Devices {
		d := fd.DeviceID
		if d == me {
			continue
		}
		rs := state(f.ID, d)
		accepted := rs == syncthing.RemoteValid || rs == syncthing.RemotePaused
		switch {
		case pcs[d] && accepted:
			return ownerSyncer
		case pcs[d]:
		case accepted:
			foreignAccepted = true
		case direct[d] && rs != syncthing.RemoteNotSharing:
			foreignWaiting = true
		}
	}
	switch {
	case foreignAccepted:
		return ownerOther
	case published:
		return ownerSyncer
	case foreignWaiting:
		return ownerUnknown
	}
	return ownerSyncer
}

// strongOwn reports whether Syncer's own settings name folder id.
func strongOwn(id string, s store.Settings) bool {
	if id == FolderID || s.Mods[id].Kind != "" || s.Launchers[id] != "" {
		return true
	}
	if _, _, ok := accounts.ParseFolderID(id); ok {
		return true
	}
	for _, lf := range s.BackupOnly {
		if lf.SyncID == id {
			return true
		}
	}
	return false
}

// remoteState asks a device's state of a folder (unknown if Syncthing can't
// say).
func remoteState(ctx context.Context, c *syncthing.Client) func(folder, device string) string {
	return func(folder, device string) string {
		comp, err := c.FolderCompletion(ctx, folder, device)
		if err != nil || comp.RemoteState == "" {
			return syncthing.RemoteUnknown
		}
		return comp.RemoteState
	}
}

// sortFolders decides whose each folder is, once (see whose), and returns
// the folders found to be Syncer's and something else's. Folders no longer
// in Syncthing are forgotten.
func sortFolders(ctx context.Context, c *syncthing.Client, me string, pcs map[string]bool, ds []syncthing.Device,
	fs []syncthing.Folder, s store.Settings, published map[string]bool) (own, other map[string]bool) {
	st := store.LoadState()
	direct := map[string]bool{}
	for _, d := range ds {
		if d.IntroducedBy == "" {
			direct[d.DeviceID] = true
		}
	}
	state := remoteState(ctx, c)
	own, other = map[string]bool{}, map[string]bool{}
	changed := false
	for _, f := range fs {
		switch {
		case st.OtherFolders[f.ID] && !strongOwn(f.ID, s):
			other[f.ID] = true
			continue
		case st.SyncerFolders[f.ID]:
			own[f.ID] = true
			continue
		}
		switch whose(f, me, pcs, direct, strongOwn(f.ID, s), published[f.ID], state) {
		case ownerSyncer:
			own[f.ID], changed = true, true
		case ownerOther:
			other[f.ID], changed = true, true
			logx.Printf("meta: leaving %s (%s) alone: it is synced with devices that don't run Syncer", cmp.Or(f.Label, f.ID), f.Path)
		}
	}
	if !changed && len(st.SyncerFolders)+len(st.OtherFolders) == len(own)+len(other) {
		return own, other
	}
	store.UpdateState(func(st *store.State) {
		st.SyncerFolders, st.OtherFolders = own, other
	})
	return own, other
}

// untangle undoes what earlier Syncers did when they took every linked
// device for a Syncer PC: Syncer's folders are taken from devices that
// don't run Syncer, and Syncer's PCs from other folders, where that device
// never accepted the folder. One that did keeps it: someone chose that. A
// device without Syncer that a Syncer PC introduced (it was never linked on
// this PC) loses Syncer's folders unless it syncs them. It returns the
// folders whose devices changed, with their new device lists.
func untangle(ctx context.Context, c *syncthing.Client, me string, pcs map[string]bool, ds []syncthing.Device,
	fs []syncthing.Folder, own, other map[string]bool) map[string][]syncthing.FolderDevice {
	introduced := map[string]bool{}
	for _, d := range ds {
		if d.IntroducedBy != "" && pcs[d.IntroducedBy] && !pcs[d.DeviceID] {
			introduced[d.DeviceID] = true
		}
	}
	state := remoteState(ctx, c)
	out := map[string][]syncthing.FolderDevice{}
	for _, f := range fs {
		if !own[f.ID] && !other[f.ID] {
			continue
		}
		keep := untangled(f, me, own[f.ID], pcs, introduced, state)
		if len(keep) == len(f.Devices) {
			continue
		}
		if err := c.PatchFolder(ctx, f.ID, map[string]any{"devices": keep}); err != nil {
			logx.Printf("meta: untangle %s: %v", f.ID, err)
			continue
		}
		var gone []string
		left := map[string]bool{}
		for _, d := range keep {
			left[d.DeviceID] = true
		}
		for _, d := range f.Devices {
			if !left[d.DeviceID] {
				gone = append(gone, d.DeviceID[:min(7, len(d.DeviceID))])
			}
		}
		logx.Printf("meta: %s is no longer offered to %s", cmp.Or(f.Label, f.ID), strings.Join(gone, ", "))
		out[f.ID] = keep
	}
	return out
}

// untangled is folder f's device list once the devices it was offered to by
// mistake are taken off (see untangle).
func untangled(f syncthing.Folder, me string, own bool, pcs, introduced map[string]bool,
	state func(folder, device string) string) []syncthing.FolderDevice {
	var keep []syncthing.FolderDevice
	for _, fd := range f.Devices {
		d := fd.DeviceID
		drop := false
		if d != me && own != pcs[d] { // Syncer's folder and another device, or the other way round
			rs := state(f.ID, d)
			accepted := rs == syncthing.RemoteValid || rs == syncthing.RemotePaused
			drop = rs == syncthing.RemoteNotSharing || own && introduced[d] && !accepted
		}
		if !drop {
			keep = append(keep, fd)
		}
	}
	return keep
}

// quietOffers stops Syncthing asking about folders Syncer PCs offer that
// Syncer deliberately doesn't sync here (skipped: folder id -> why).
// Should that change, Syncer adds the folder itself, and Syncthing then
// forgets the ignore.
func quietOffers(ctx context.Context, c *syncthing.Client, pcs map[string]bool, skipped map[string]string, have map[string]bool) {
	if len(skipped) == 0 {
		return
	}
	pending, err := c.PendingFolders(ctx)
	if err != nil {
		return
	}
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		reason, ok := skipped[id]
		if !ok || have[id] {
			continue
		}
		for dev, offer := range pending[id].OfferedBy {
			if !pcs[dev] {
				continue
			}
			if err := c.IgnoreFolder(ctx, dev, id, offer.Label); err != nil {
				logx.Printf("meta: ignore offer of %s: %v", id, err)
				continue
			}
			logx.Printf("meta: %s isn't synced here (%s): Syncthing won't ask about it", cmp.Or(offer.Label, id), reason)
		}
	}
}
