<p align="center"><img src="build/appicon.png" width="96" alt=""></p>

<h1 align="center">Syncer</h1>

<p align="center">Keep your PC game saves in sync between your Windows PCs, with a versioned backup in Google Drive.</p>

---

Syncer is a small desktop app built on two tools that already work well:

- **[Syncthing](https://syncthing.net)** copies saves directly between your PCs. There's no account and no cloud in between.
- **[Google Drive for desktop](https://www.google.com/drive/download/)** handles the upload. Syncer copies your saves into `My Drive\GameSaveBackup`, and Drive uploads them from there.

## Features

- **Finds your games, and keeps finding them.** It checks ~14k games from the [Ludusavi manifest](https://github.com/mtkennerly/ludusavi-manifest) (save locations from PCGamingWiki) against your PC in under a second. Newly installed games start syncing on their own (at logon, every few hours, and hourly while the window is open). Games you stop syncing are never added back. Save folders over 1 GB wait for a click; raise or remove that limit in Settings if your saves are bigger. You can also add any folder yourself.
- **Knows what Steam Cloud really covers.** A game supporting Steam Cloud isn't enough: Syncer checks that your Steam account on this PC actually keeps that game's saves in the cloud. Copies installed outside Steam, games owned by another account, or cloud switched off are synced like any other. You can choose to include confirmed Steam Cloud games too.
- **Links PCs with one ID.** Paste the other PC's device ID (or accept its request) and every synced game shows up there at the correct local path, even if the user name or Documents location is different.
- **Backs up with history.** Only changed files get copied. A file that changes or gets deleted is moved to `.versions\<game>\<time>\` and kept for 30 days by default. If a save folder suddenly turns up empty, Syncer won't wipe the backup.
- **Restores any save.** You can restore the latest backup or any earlier point. Your current files are saved as a restore point first, so a restore can be undone.
- **Lives in the tray if you want.** Settings can start Syncer with Windows (straight into the tray) and keep it running there when you close the window. The tray menu opens it, starts a backup or quits.
- **Runs without the window.** A scheduled task (`\Syncer-Background`) starts Syncthing if it isn't running, applies changes from your other PCs and runs the backup at logon and every few hours.
- **Never picks a save behind your back.** Before a PC starts syncing a game it already has saves for, those saves are stored as a restore point in the backup (or in `%LOCALAPPDATA%\Syncer\snapshots` when no backup folder is available). When two PCs changed the same save, the game shows **2 versions**: pick which one to keep, and the other goes into the backup history instead of being deleted.
- **Protects synced saves.** Every synced folder uses Syncthing's staggered versioning, so a corrupted save coming from another PC doesn't overwrite the good copy.
- **Light and dark mode.** It follows your Windows setting (you can override it), and uses the Mica backdrop on Windows 11.

## Install

1. Download **`Syncer-amd64-installer.exe`** from [Releases](https://github.com/ApolloF/syncer/releases) and run it. It installs just for your user (no admin prompt) into `%LOCALAPPDATA%\Programs\Syncer` and adds Start menu and desktop shortcuts. Prefer no installer? `Syncer.exe` from the same release is portable. Keep it in a permanent folder, since the background task points at it.
2. Open Syncer. If Syncthing isn't installed yet, the Overview has a one-click install (via `winget`).
3. Install [Google Drive for desktop](https://www.google.com/drive/download/) and sign in. Syncer finds it on its own. Signed in with more than one Google account (e.g. `G:` and `H:`)? Pick which one gets the backups under **Backup → Account**.
4. Your games start syncing automatically. Check **Games → Found on this PC** for anything else you want, such as unrecognized folders.

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

## Develop

Requirements: Go 1.26+, Node 22+, [Wails v2](https://wails.io) (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`).

```bash
wails dev          # live-reloading app
go test ./...      # backend tests
wails build        # build/bin/Syncer.exe
```

The backend lives in `internal/`: `syncthing` (REST client), `meta` (cross-PC folder sharing), `discover` (manifest + scan), `backup` (mirror/versions/restore), `tasks` (Task Scheduler) and `paths` (portable paths). The Svelte 5 frontend is in `frontend/src`.

## Uninstall

Use *Settings → Apps → Syncer → Uninstall*. This removes the app and its scheduled task. Syncthing, your synced saves and your Drive backups are left untouched.
