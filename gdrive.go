package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/gdrive"
	"github.com/ApolloF/syncer/internal/logx"
	"github.com/ApolloF/syncer/internal/paths"
	"github.com/ApolloF/syncer/internal/store"
)

// Without Google Drive for desktop, Syncer can sign in to Google itself and
// keep the backup folder in sync with Drive (see internal/gdrive). The
// backup is then written into a folder on this PC, exactly as it would be
// into Drive for desktop's, and synced with Drive before and after every
// backup and before looking for newer saves on other PCs.

// The OAuth client this build signs in as, set at release builds
// (-ldflags "-X main.gdriveClientID=… -X main.gdriveClientSecret=…"). Builds
// without one don't offer signing in to Google.
var (
	gdriveClientID     = ""
	gdriveClientSecret = ""
)

// backendGoogle is Settings.BackupBackend when Syncer signs in to Google.
const backendGoogle = "google"

func gdriveConfig() gdrive.Config {
	return gdrive.Config{ClientID: gdriveClientID, ClientSecret: gdriveClientSecret}
}

// googleDir is the folder on this PC kept in sync with GameSaveBackup in Drive.
func googleDir() string {
	return filepath.Join(paths.Root(paths.Local), "Syncer", "GoogleDrive", gdrive.RootName)
}

// GoogleView is the Google sign-in state for the Backup page.
type GoogleView struct {
	Available bool      `json:"available"` // this build can sign in to Google
	SignedIn  bool      `json:"signedIn"`
	Account   string    `json:"account"`
	Synced    time.Time `json:"synced,omitzero"` // last sync with Drive
	Error     string    `json:"error,omitempty"` // of the last sync
}

func googleView(s store.Settings) GoogleView {
	v := GoogleView{Available: gdriveConfig().Configured()}
	if s.BackupBackend != backendGoogle {
		return v
	}
	if t, err := gdrive.LoadToken(); err == nil {
		v.SignedIn, v.Account = true, t.Account
	}
	st := store.LoadState()
	v.Synced, v.Error = st.GoogleSynced, st.GoogleError
	return v
}

var googleSyncMu sync.Mutex

// googleSync syncs the backup folder with Drive when backups go to Google
// through Syncer's own sign-in; otherwise it does nothing. With maxAge, a
// sync that ran that recently is enough. It doesn't run while a backup does.
func googleSync(ctx context.Context, maxAge time.Duration) error {
	s := store.LoadSettings()
	if s.BackupBackend != backendGoogle {
		return nil
	}
	googleSyncMu.Lock()
	defer googleSyncMu.Unlock()
	if maxAge > 0 && time.Since(store.LoadState().GoogleSynced) < maxAge {
		return nil
	}
	tok, err := gdrive.LoadToken()
	if err != nil {
		recordGoogle(err)
		return err
	}
	unlock, err := backup.Lock()
	if err != nil {
		return err // a backup is running; it syncs when it's done
	}
	defer unlock()
	c := gdrive.NewClient(gdriveConfig(), tok, gdrive.SaveToken)
	rep, err := gdrive.Sync(ctx, c, googleDir())
	if err == nil && len(rep.Errors) > 0 {
		err = errors.New(rep.Errors[0])
	}
	if n := rep.Up + rep.Down + rep.Moved + rep.DeletedHere + rep.DeletedThere; n > 0 || err != nil {
		logx.Printf("Google Drive: %d up, %d down, %d moved, %d deleted here, %d deleted there, %d kept in history, %d error(s)",
			rep.Up, rep.Down, rep.Moved, rep.DeletedHere, rep.DeletedThere, rep.Kept, len(rep.Errors))
	}
	for _, n := range rep.Notes {
		logx.Printf("Google Drive: %s", n)
	}
	if errors.Is(err, gdrive.ErrSignedOut) {
		_ = gdrive.DeleteToken()
	}
	recordGoogle(err)
	return err
}

func recordGoogle(err error) {
	store.UpdateState(func(st *store.State) {
		if err != nil {
			st.GoogleError = err.Error()
			return
		}
		st.GoogleSynced, st.GoogleError = time.Now(), ""
	})
}

// SignInGoogle signs in to Google in the browser and makes backups go to
// the account's Drive through Syncer's own sync.
func (a *App) SignInGoogle() (string, error) {
	cfg := gdriveConfig()
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
	defer cancel()
	tok, err := gdrive.SignIn(ctx, cfg, func(u string) { runtime.BrowserOpenURL(a.ctx, u) })
	if err != nil {
		return "", err
	}
	account, err := gdrive.NewClient(cfg, tok, nil).Account(ctx)
	if err != nil {
		return "", err
	}
	if prev := gdrive.LoadState().Account; prev != "" && !strings.EqualFold(prev, account) {
		// The copy here is the other account's backup: it mustn't go up
		// into this one. It's kept aside (that account's Drive has it too).
		if _, err := os.Stat(googleDir()); err == nil {
			aside := googleDir() + " (" + safeName(prev) + ")"
			if err := os.Rename(googleDir(), aside); err != nil {
				return "", fmt.Errorf("can't set aside the backup of %s: %w", prev, err)
			}
			logx.Printf("set aside the backup copy of %s in %s", prev, aside)
		}
		gdrive.ForgetState()
	}
	gdrive.SetAccount(account)
	tok.Account = account
	if err := gdrive.SaveToken(tok); err != nil {
		return "", err
	}
	if _, err := store.UpdateSettings(func(s *store.Settings) { s.BackupBackend, s.BackupRoot = backendGoogle, "" }); err != nil {
		return "", err
	}
	recordGoogle(nil)
	logx.Printf("signed in to Google as %s; backups go to its Drive", account)
	runtime.EventsEmit(a.ctx, "changed")
	go func() {
		if err := a.BackupNow(); err != nil && !errors.Is(err, backup.ErrBusy) {
			logx.Printf("backup after signing in: %v", err)
		}
	}()
	return account, nil
}

// SignOutGoogle signs out of Google and goes back to Google Drive for
// desktop. The backup already in Drive stays there; the copy on this PC too
// (signing in to the same account again carries on with it).
func (a *App) SignOutGoogle() error {
	if tok, err := gdrive.LoadToken(); err == nil {
		ctx, cancel := a.callCtx()
		_ = gdrive.Revoke(ctx, tok)
		cancel()
	}
	if err := gdrive.DeleteToken(); err != nil {
		return err
	}
	if _, err := store.UpdateSettings(func(s *store.Settings) { s.BackupBackend = "" }); err != nil {
		return err
	}
	logx.Printf("signed out of Google")
	runtime.EventsEmit(a.ctx, "changed")
	return nil
}

// safeName makes an account name usable in a folder name.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, s)
}
