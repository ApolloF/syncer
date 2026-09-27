package mods

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Manifest is a Vortex deployment manifest (vortex.deployment*.json), which
// Vortex writes into every folder it deploys mods to.
type Manifest struct {
	Version          int            `json:"version"`
	Instance         string         `json:"instance"`
	DeploymentMethod string         `json:"deploymentMethod"`
	GameID           string         `json:"gameId"`
	StagingPath      string         `json:"stagingPath"`
	TargetPath       string         `json:"targetPath"`
	Files            []DeployedFile `json:"files"`

	// Dir is the folder the manifest was found in (the deployment target).
	Dir string `json:"-"`
	// File is the manifest's own file name.
	File string `json:"-"`
}

// DeployedFile is one file Vortex deployed.
type DeployedFile struct {
	RelPath string `json:"relPath"` // relative to the deployment target
	Source  string `json:"source"`  // the mod it came from
}

// Deployment methods Vortex can use.
const (
	MethodHardlink = "hardlink_activator"
	MethodMove     = "move_activator"
	MethodSymlink  = "symlink_activator"
)

const (
	manifestPrefix = "vortex.deployment"
	maxManifest    = 64 << 20
	// maxScanDirs caps how many folders of one game are looked at for
	// deployment manifests.
	maxScanDirs = 3000
	// manifestDepth is how deep below a game's folder manifests are looked
	// for: Data (1) for Bethesda games, archive\pc\mod (3) for Cyberpunk.
	manifestDepth = 3
)

// isManifestName reports whether name is a Vortex deployment manifest.
func isManifestName(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, manifestPrefix) && strings.HasSuffix(n, ".json")
}

// ReadManifest parses one deployment manifest.
func ReadManifest(path string) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxManifest+1))
	if err != nil {
		return Manifest{}, err
	}
	if len(b) > maxManifest {
		return Manifest{}, errors.New("deployment manifest too big")
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, err
	}
	m.Dir, m.File = filepath.Dir(path), filepath.Base(path)
	m.GameID = strings.ToLower(strings.TrimSpace(m.GameID))
	return m, nil
}

// found manifest plus the game folder it lies in.
type gameManifest struct {
	Manifest
	GameDir string
}

// findManifests looks for deployment manifests in each game folder and the
// folders below it (not following links).
func findManifests(gameDirs []string) []gameManifest {
	var out []gameManifest
	for _, gd := range gameDirs {
		gd = filepath.Clean(gd)
		type level struct {
			dir   string
			depth int
		}
		queue := []level{{gd, 0}}
		seen := 0
		for len(queue) > 0 && seen < maxScanDirs {
			cur := queue[0]
			queue = queue[1:]
			seen++
			es, err := os.ReadDir(cur.dir)
			if err != nil {
				continue
			}
			for _, e := range es {
				name := e.Name()
				switch {
				case e.Type().IsRegular() && isManifestName(name):
					m, err := ReadManifest(filepath.Join(cur.dir, name))
					if err == nil && m.GameID != "" {
						out = append(out, gameManifest{Manifest: m, GameDir: gd})
					}
				case e.IsDir() && cur.depth < manifestDepth && !skipScanDir(name):
					sub := filepath.Join(cur.dir, name)
					if !isReparse(sub) {
						queue = append(queue, level{sub, cur.depth + 1})
					}
				}
			}
		}
	}
	return out
}

// skipScanDir are folders that never hold a deployment target but can be big.
func skipScanDir(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, ".") || n == "__overlay" || n == "_commonredist" || n == "redist"
}

// isReparse reports whether p is a symlink, junction or other reparse point.
func isReparse(p string) bool {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return true
	}
	a, err := windows.GetFileAttributes(u)
	if err != nil {
		return false
	}
	return a&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
