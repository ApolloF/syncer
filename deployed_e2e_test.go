package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/mods"
	"github.com/ApolloF/syncer/internal/syncthing"
)

// TestDeployedE2E applies deployed mods between two real Syncthing
// instances on localhost. It only runs with SYNCER_E2E_SYNCTHING set to a
// syncthing.exe; nothing of this PC's own Syncthing or Syncer is touched.
func TestDeployedE2E(t *testing.T) {
	exe := os.Getenv("SYNCER_E2E_SYNCTHING")
	if exe == "" {
		t.Skip("set SYNCER_E2E_SYNCTHING to a syncthing.exe to run")
	}
	base := testDir(t)
	a := startST(t, exe, base, "A", 18484, 22101)
	b := startST(t, exe, base, "B", 18485, 22102)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	must(t, a.c.AddDevice(ctx, syncthing.Device{DeviceID: b.id, Name: "B", Addresses: []string{"tcp://127.0.0.1:22102"}}))
	must(t, b.c.AddDevice(ctx, syncthing.Device{DeviceID: a.id, Name: "A", Addresses: []string{"tcp://127.0.0.1:22101"}}))

	// A, the source: two mods deployed next to the game's own files.
	dA, dB := filepath.Join(base, "A", "Data"), filepath.Join(base, "B", "Data")
	put(t, filepath.Join(dA, "Skyrim.esm"), "vanilla A")
	put(t, filepath.Join(dA, "SkyUI_SE.esp"), "skyui")
	put(t, filepath.Join(dA, "meshes", "sky.nif"), "sky mesh")
	manifest := mods.Manifest{Version: 1, DeploymentMethod: mods.MethodHardlink, GameID: "skyrimse",
		Files: []mods.DeployedFile{{RelPath: "SkyUI_SE.esp"}, {RelPath: `meshes\sky.nif`}}}
	mb, _ := json.Marshal(manifest)
	put(t, filepath.Join(dA, "vortex.deployment.json"), string(mb))
	inv, err := mods.BuildInventory("skyrimse", dA, filepath.Dir(dA), nil)
	must(t, err)
	inv.Folder, inv.Gen = "sky-data", 1

	// B, the receiver: the game's own files, an outdated copy of one mod
	// file, and a file of its own inside a mod folder.
	put(t, filepath.Join(dB, "Skyrim.esm"), "vanilla B")
	put(t, filepath.Join(dB, "meshes", "sky.nif"), "old mesh")
	put(t, filepath.Join(dB, "meshes", "mine.nif"), "B's own")
	outside, err := mods.Outside(dB, mods.Scope(&inv))
	must(t, err)

	scope := append(withSyncIgnores(mods.KindDeployed, nil), scopeLines(nil, &inv)...)
	put(t, filepath.Join(dA, ".stignore"), strings.Join(scope, "\n")+"\n")
	addST(t, ctx, a, "sky-data", dA, "sendonly", false, b.id)
	addST(t, ctx, b, "sky-data", dB, "receiveonly", true, a.id)

	p := &applyPlan{id: "sky-data", label: "Skyrim", path: dB, inv: inv, src: meta.Source{Device: a.id, Name: "A"}}
	p.diff = mods.DiffLocal(dB, nil, inv)
	if len(p.diff.Added) != 1 || len(p.diff.Changed) != 1 {
		t.Fatalf("diff = %+v", p.diff)
	}
	if err := letSync(ctx, b.c, p, scope); err != nil {
		t.Fatalf("letSync: %v", err)
	}
	if cs := mods.CheckApplied(dB, inv, nil); !mods.Passed(cs) {
		t.Errorf("applied: %v", mods.Failed(cs))
	}
	after, _ := mods.Outside(dB, mods.Scope(&inv))
	if c := mods.CheckOutside(outside, after); !c.OK {
		t.Errorf("the receiver's own files changed: %s", c.Detail)
	}
	if m, _ := filepath.Glob(filepath.Join(dB, "meshes", "*sync-conflict*")); len(m) > 0 {
		t.Errorf("conflict copies left in the game folder: %v", m)
	}
	must(t, b.c.PatchFolder(ctx, "sky-data", map[string]any{"paused": true}))

	// A source whose files no longer match its own list (its index lacks a
	// file the list still has): the receiver must stop, not delete its copy.
	must(t, os.Remove(filepath.Join(dA, "meshes", "sky.nif")))
	must(t, a.c.Rescan(ctx, "sky-data"))
	time.Sleep(3 * time.Second)
	p2 := &applyPlan{id: "sky-data", label: "Skyrim", path: dB, inv: inv, prev: &inv, src: p.src}
	p2.diff = mods.DiffLocal(dB, &inv, inv) // nothing to do, as far as the list says
	sctx, scancel := context.WithTimeout(ctx, 150*time.Second)
	err = letSync(sctx, b.c, p2, scope)
	scancel()
	if err == nil || !strings.Contains(err.Error(), "match") {
		t.Errorf("a source not matching its list: letSync = %v, want a mismatch error", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dB, "meshes", "sky.nif")); string(b) != "sky mesh" {
		t.Errorf("the receiver's copy was changed before the mismatch was found: %q", b)
	}
	t.Logf("mismatch run: %v", err)
}

type stInst struct {
	id  string
	c   *syncthing.Client
	cmd *exec.Cmd
}

func startST(t *testing.T, exe, base, name string, gui, listen int) *stInst {
	t.Helper()
	home := filepath.Join(base, "home-"+name)
	if out, err := exec.Command(exe, "generate", "--home="+home, "--no-port-probing").CombinedOutput(); err != nil {
		t.Fatalf("generate: %v %s", err, out)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", gui)
	cmd := exec.Command(exe, "serve", "--home="+home, "--gui-address="+addr, "--gui-apikey=key-"+name, "--no-browser", "--no-restart", "--no-upgrade")
	must(t, cmd.Start())
	c, err := syncthing.NewAt(addr, "key-"+name)
	must(t, err)
	s := &stInst{c: c, cmd: cmd}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = c.Shutdown(ctx)
		cancel()
		_ = cmd.Wait()
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for {
		if st, err := c.Status(ctx); err == nil {
			s.id = st.MyID
			break
		}
		if ctx.Err() != nil {
			t.Fatal("syncthing didn't start")
		}
		time.Sleep(300 * time.Millisecond)
	}
	must(t, c.PatchOptions(ctx, map[string]any{"globalAnnounceEnabled": false, "localAnnounceEnabled": false, "relaysEnabled": false,
		"natEnabled": false, "urAccepted": -1, "crashReportingEnabled": false,
		"listenAddresses": []string{fmt.Sprintf("tcp://127.0.0.1:%d", listen)}}))
	return s
}

func addST(t *testing.T, ctx context.Context, s *stInst, id, path, typ string, paused bool, peer string) {
	t.Helper()
	f := map[string]any{"id": id, "label": id, "path": path, "type": typ, "paused": paused, "fsWatcherEnabled": false,
		"rescanIntervalS": 3600, "ignorePerms": true, "devices": devicesFor(s.id, []string{peer})}
	if typ == "receiveonly" {
		f["maxConflicts"] = 0
	}
	must(t, s.c.AddFolder(ctx, f))
}

func put(t *testing.T, p, content string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.WriteFile(p, []byte(content), 0o644))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
