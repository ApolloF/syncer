<p align="center"><img src="build/appicon.png" width="96" alt=""></p>

<h1 align="center">Syncer</h1>

<p align="center">Keep your PC game saves in sync between your Windows PCs, with a versioned backup in Google Drive.</p>

---

Syncer is a small desktop app built on two tools that already work well:

- **[Syncthing](https://syncthing.net)** copies saves directly between your PCs. There's no account and no cloud in between.
- **[Google Drive for desktop](https://www.google.com/drive/download/)** handles the upload. Syncer copies your saves into `My Drive\GameSaveBackup`, and Drive uploads them from there.

## Features

- **Finds your games.** It checks ~14k games from the [Ludusavi manifest](https://github.com/mtkennerly/ludusavi-manifest) (save locations from PCGamingWiki) against your PC in under a second. Games that Steam Cloud already covers are hidden by default. You can also add any folder yourself.
- **Links PCs with one ID.** Paste the other PC's device ID (or accept its request) and every synced game shows up there at the correct local path, even if the user name or Documents location is different.
- **Backs up with history.** Only changed files get copied. A file that changes or gets deleted is moved to `.versions\<game>\<time>\` and kept for 30 days by default. If a save folder suddenly turns up empty, Syncer won't wipe the backup.
- **Restores any save.** You can restore the latest backup or any earlier point. Your current files are saved as a restore point first, so a restore can be undone.
- **Runs without the window.** A scheduled task (`\Syncer-Background`) starts Syncthing if it isn't running, applies changes from your other PCs and runs the backup at logon and every few hours.
- **Protects synced saves.** Every synced folder uses Syncthing's staggered versioning, so a corrupted save coming from another PC doesn't overwrite the good copy.
- **Back up without syncing.** Turn off *Sync* for a game (or pick *Back up only* when adding one) and it keeps being backed up from this PC without being shared with your other PCs.
- **Only installed games.** With *Settings → Only sync installed games* on, games from your other PCs are added only when they're installed here (Steam, Epic, GOG, Ubisoft, Xbox and regular installers are recognized). *Games → On other PCs* lists everything else so you can add it by hand.
- **Stays out of your way while you play.** Backups run at background CPU and disk priority, and the automatic backup waits while a full-screen app or an installed game is in the foreground (*Backup → Pause while playing*).
- **Undo.** Removing a game takes it out of Syncer and cleans up Syncthing's marker files (optionally deleting its backup too). *Settings → Undo everything* stops all syncing on this PC, and can also unlink your PCs, stop or uninstall Syncthing, and stop or delete backups. Save files are never deleted.
- **Light and dark mode.** It follows your Windows setting (you can override it), and uses the Mica backdrop on Windows 11.

## Install

1. Download **`Syncer-amd64-installer.exe`** from [Releases](https://github.com/ApolloF/syncer/releases) and run it. It installs just for your user (no admin prompt) into `%LOCALAPPDATA%\Programs\Syncer` and adds Start menu and desktop shortcuts. Prefer no installer? `Syncer.exe` from the same release is portable. Keep it in a permanent folder, since the background task points at it.
2. Open Syncer. If Syncthing isn't installed yet, the Overview has a one-click install (via `winget`).
3. Install [Google Drive for desktop](https://www.google.com/drive/download/) and sign in. Syncer finds it on its own.
4. Go to **Games → Found on this PC** and click **Sync** on the games you want.

### Linking a second PC

1. Install Syncer on the second PC.
2. On one PC, open **Devices** and paste the other PC's ID.
3. Accept the request that appears on the other PC.

That's all. Folders are published through a small shared folder (`syncer-meta`), where each PC writes only its own file, so no write conflicts can happen. Every PC adds whatever folders it's missing.

## How it works

```
 PC A                                  PC B
 ┌─────────────┐   Syncthing (P2P)   ┌─────────────┐
 │ save folders│◄───────────────────►│ save folders│
 │ syncer-meta │◄───────────────────►│ syncer-meta │  ← each PC's folder list, portable paths
 └──────┬──────┘                     └─────────────┘
        │ Syncer.exe --background (Task Scheduler)
        ▼
 G:\My Drive\GameSaveBackup\<game>\…      ← mirror
 G:\My Drive\GameSaveBackup\.versions\…   ← replaced/deleted files, pruned after N days
        │ Google Drive for desktop
        ▼
   Google Drive
```

Paths are stored relative to Windows known folders (Documents, Saved Games, AppData\Roaming/Local/LocalLow, profile), so they resolve correctly on every PC. That includes a Documents folder redirected to OneDrive.

Syncer stores its settings in `%APPDATA%\Syncer`. It reads Syncthing's API key from Syncthing's own `config.xml` whenever it needs it and never saves a copy.

### Security

- Folder lists from other PCs are treated as untrusted. Ids and paths are validated, and folders like your whole profile, `.ssh`, browser profiles, the Startup folder or Syncthing's own settings are never shared, even if another PC asks for them.
- Syncer talks to Syncthing only on this PC, or over HTTPS pinned to Syncthing's own certificate. It never sends the API key in the clear over the network.
- System tools (`schtasks`, `tasklist`, `explorer`) are started by their full path, and the scheduled task runs with your normal user rights (no elevation).

## Develop

Requirements: Go 1.26+, Node 22+, [Wails v2](https://wails.io) (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`).

```bash
wails dev          # live-reloading app
go test ./...      # backend tests
wails build        # build/bin/Syncer.exe
```

The backend lives in `internal/`: `syncthing` (REST client), `meta` (cross-PC folder sharing), `discover` (manifest, scan, installed games), `backup` (mirror/versions/restore), `tasks` (Task Scheduler), `paths` (portable paths, sync safety rules) and `winx` (Windows priority, full-screen and foreground checks). The Svelte 5 frontend is in `frontend/src`.

## Uninstall

To stop syncing but keep the app, use *Settings → Undo everything* in Syncer first. Then uninstall with *Settings → Apps → Syncer → Uninstall*. This removes the app and its scheduled task. Syncthing, your synced saves and your Drive backups are left untouched.
