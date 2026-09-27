package mods

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ApolloF/syncer/internal/paths"
)

// testDir is a folder under %LOCALAPPDATA% (the temp folder is one Syncer
// refuses to sync, so t.TempDir can't stand in for a game or Vortex folder).
func testDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(paths.Root(paths.Local), "syncer-modtest-"+time.Now().Format("150405.000000"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeVortex lays out a Vortex data folder and one game with a deployment,
// and points the package at them.
type fakeVortex struct {
	base, root, game, data, staging string
}

func setupVortex(t *testing.T, customStaging bool) fakeVortex {
	t.Helper()
	base := testDir(t)
	fv := fakeVortex{base: base, root: filepath.Join(base, "Vortex"), game: filepath.Join(base, "Games", "Skyrim Special Edition")}
	fv.data = filepath.Join(fv.game, "Data")
	fv.staging = filepath.Join(fv.root, "skyrimse", "mods")
	if customStaging {
		fv.staging = filepath.Join(base, "Vortex Mods", "skyrimse")
	}
	write(t, filepath.Join(fv.staging, stagingMarker), `{"instance":"abc"}`)
	write(t, filepath.Join(fv.staging, "SkyUI", "SkyUI_SE.esp"), "plugin")
	write(t, filepath.Join(fv.root, "skyrimse", "profiles", "p1", "plugins.txt"), "*SkyUI_SE.esp\n")
	write(t, filepath.Join(fv.root, "downloads", "skyrimse", "a.7z"), "x")
	write(t, filepath.Join(fv.root, "state.v2", "CURRENT"), "x")
	write(t, filepath.Join(fv.game, "SkyrimSE.exe"), "exe")
	write(t, filepath.Join(fv.data, "Skyrim.esm"), "vanilla")
	write(t, filepath.Join(fv.data, "SkyUI_SE.esp"), "plugin")
	m := Manifest{Version: 1, Instance: "abc", DeploymentMethod: MethodHardlink, GameID: "skyrimse",
		StagingPath: fv.staging, TargetPath: fv.data, Files: []DeployedFile{{RelPath: "SkyUI_SE.esp", Source: "SkyUI"}}}
	b, _ := json.Marshal(m)
	write(t, filepath.Join(fv.data, "vortex.deployment.json"), string(b))

	oldRoot, oldGames := vortexRoot, gameDirs
	vortexRoot = func() string { return fv.root }
	gameDirs = func() []string { return []string{fv.game} }
	Forget()
	t.Cleanup(func() { vortexRoot, gameDirs = oldRoot, oldGames; Forget() })
	return fv
}

func byKind(fs []Found) map[string]Found {
	m := map[string]Found{}
	for _, f := range fs {
		m[f.Kind] = f
	}
	return m
}

func TestDetectDefaultStaging(t *testing.T) {
	fv := setupVortex(t, false)
	got := byKind(Vortex{}.Detect())
	if len(got) != 3 {
		t.Fatalf("found %d kinds, want 3: %+v", len(got), got)
	}
	st := got[KindStaging]
	if st.Path != fv.staging || st.Root != "vortex:skyrimse" || st.Rel != "staging" || st.GameName != "Skyrim Special Edition" {
		t.Errorf("staging = %+v", st)
	}
	if p := got[KindProfiles].Path; p != filepath.Join(fv.root, "skyrimse", "profiles") {
		t.Errorf("profiles = %s", p)
	}
	d := got[KindDeployed]
	if d.Path != fv.data || d.Root != "game:skyrimse" || d.Rel != "Data" || d.GameDir != fv.game || len(d.Warn) != 0 {
		t.Errorf("deployed = %+v", d)
	}
}

func TestDetectCustomStaging(t *testing.T) {
	fv := setupVortex(t, true)
	st := byKind(Vortex{}.Detect())[KindStaging]
	if st.Path != fv.staging {
		t.Fatalf("staging = %q, want %q", st.Path, fv.staging)
	}
	p, err := Resolve("vortex:skyrimse", "staging")
	if err != nil || p != fv.staging {
		t.Errorf("Resolve = %q, %v", p, err)
	}
}

func TestDetectNeedsMarker(t *testing.T) {
	fv := setupVortex(t, false)
	if err := os.Remove(filepath.Join(fv.staging, stagingMarker)); err != nil {
		t.Fatal(err)
	}
	if _, ok := byKind(Vortex{}.Detect())[KindStaging]; ok {
		t.Error("staging folder without Vortex's marker was detected")
	}
	if _, err := Resolve("vortex:skyrimse", "staging"); !errors.Is(err, ErrGameMissing) {
		t.Errorf("Resolve without marker = %v, want ErrGameMissing", err)
	}
}

func TestResolve(t *testing.T) {
	fv := setupVortex(t, false)
	cases := []struct {
		root, rel string
		want      string
		err       error
	}{
		{"vortex:skyrimse", "staging", fv.staging, nil},
		{"vortex:skyrimse", "profiles", filepath.Join(fv.root, "skyrimse", "profiles"), nil},
		{"game:skyrimse", "Data", fv.data, nil},
		{"vortex:fallout4", "staging", "", ErrGameMissing},
		{"game:fallout4", "Data", "", ErrGameMissing},
		{"vortex:skyrimse", "../..", "", ErrUnsafe},
		{"vortex:skyrimse", "downloads", "", ErrUnsafe},
		{"game:skyrimse", "../..", "", ErrUnsafe},
		{"game:skyrimse", `C:\Windows`, "", ErrUnsafe},
		{"game:skyrimse", `Data\..\..\x`, "", ErrUnsafe},
		{"vortex:..", "staging", "", ErrUnsafe},
		{"vortex:Sky rim", "staging", "", ErrUnsafe},
		{"vortex:state.v2", "staging", "", ErrUnsafe},
		{"documents", "staging", "", ErrUnsafe},
	}
	for _, c := range cases {
		got, err := Resolve(c.root, c.rel)
		if got != c.want || !errors.Is(err, c.err) {
			t.Errorf("Resolve(%q, %q) = %q, %v; want %q, %v", c.root, c.rel, got, err, c.want, c.err)
		}
	}
}

func TestKindOf(t *testing.T) {
	cases := map[[2]string]string{
		{"vortex:skyrimse", "staging"}:  KindStaging,
		{"vortex:skyrimse", "profiles"}: KindProfiles,
		{"game:skyrimse", "Data"}:       KindDeployed,
		{"vortex:skyrimse", "Data"}:     "",
		{"roaming", "staging"}:          "",
		{"vortex:../x", "staging"}:      "",
	}
	for in, want := range cases {
		got, _ := KindOf(in[0], in[1])
		if got != want {
			t.Errorf("KindOf(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestSymlinkDeploymentWarns(t *testing.T) {
	fv := setupVortex(t, false)
	m, err := ReadManifest(filepath.Join(fv.data, "vortex.deployment.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.DeploymentMethod = MethodSymlink
	b, _ := json.Marshal(m)
	write(t, filepath.Join(fv.data, "vortex.deployment.json"), string(b))
	if d := byKind(Vortex{}.Detect())[KindDeployed]; len(d.Warn) == 0 {
		t.Error("symlink deployment has no warning")
	}
}

func TestManagerRunning(t *testing.T) {
	if !ManagerRunning([]string{`C:\x\a.exe`, `C:\Program Files\Black Tree Gaming Ltd\Vortex\Vortex.exe`}) {
		t.Error("Vortex.exe not seen as running")
	}
	if ManagerRunning([]string{`C:\x\NotVortex.exe`}) {
		t.Error("another exe seen as Vortex")
	}
}

func TestSplitKey(t *testing.T) {
	f := Found{Root: "game:skyrimse", Rel: "Data/sub"}
	r, rel, ok := SplitKey(f.Key())
	if !ok || r != f.Root || rel != f.Rel {
		t.Errorf("SplitKey(%q) = %q, %q, %v", f.Key(), r, rel, ok)
	}
	if _, _, ok := SplitKey("roaming/Saves"); ok {
		t.Error("save root accepted as a mod key")
	}
}
