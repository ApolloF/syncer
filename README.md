<p align="center"><img src="build/appicon.png" width="96" alt=""></p>

<h1 align="center">Syncer</h1>

<p align="center">Keep your PC game saves in sync between your Windows PCs, with a versioned backup in your own Google Drive.</p>

<p align="center">
  <a href="https://github.com/ApolloF/syncer/releases/latest"><img src="https://img.shields.io/github/v/release/ApolloF/syncer?label=download" alt="Latest release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/ApolloF/syncer" alt="License: AGPL-3.0"></a>
  <a href="https://github.com/ApolloF/syncer/actions/workflows/build.yml"><img src="https://github.com/ApolloF/syncer/actions/workflows/build.yml/badge.svg" alt="Build"></a>
</p>

<!--
Screenshots to take before launch (made-up games, PC names and accounts only; no real
user names, emails or scene-group names). Save them in docs/images/ and replace this comment.

1. docs/images/syncer-demo.gif (hero, under this comment): 15-25 s, 1280x720, 15 fps, under 5 MB.
   Games page -> open a game -> Restore -> pick an earlier restore point -> Restore -> "Restored".
2. docs/images/syncer-games.png: Games page with 6-8 games, a mix of Synced, Backup only,
   "Also in Steam Cloud" and one "2 versions" badge.
3. docs/images/syncer-restore.png: Restore dialog with several restore points from two PCs.
4. docs/images/syncer-conflict.png: the "2 versions" dialog, both PCs' files side by side.
5. docs/images/syncer-devices.png: Devices page with this PC's ID and two linked PCs
   (one on this network, one over the internet).
6. docs/images/syncer-backup.png: Backup page signed in with Google, showing the GameSaveBackup
   folder, last backup time and the "only the files it creates" wording.

Shots 2, 3 and 5 already exist on the local branch docs/go-to-market (commit e69c801).
Then add, in the Install section, once the winget PR is merged:  winget install ApolloF.Syncer
-->

**[Download Syncer](https://github.com/ApolloF/syncer/releases/latest/download/Syncer-amd64-installer.exe)** for Windows 10 and 11 (64-bit). Free and open source, no account, no telemetry.

- Finds the saves of about 14,000 games on its own and syncs them between your PCs, end-to-end encrypted.
- Keeps a versioned backup in your own Google Drive, with restore points you can roll back to.
- Never overwrites a save behind your back: when two PCs changed the same save, you pick which one to keep.
- Leaves what Steam Cloud, OneDrive and Ubisoft Connect already sync to them.
- Holds syncing while a game runs, so no save changes under a running game.

## Install

1. Download **`Syncer-amd64-installer.exe`** from [Releases](https://github.com/ApolloF/syncer/releases/latest) and run it. It installs just for your user (no admin prompt) into `%LOCALAPPDATA%\Programs\Syncer`. Prefer no installer? `Syncer.exe` from the same release is portable; keep it in a permanent folder, since the background task points at it.
2. Open Syncer. If [Syncthing](https://syncthing.net) isn't installed yet, the Overview installs it with one click (via `winget`).
3. For the backup, install [Google Drive for desktop](https://www.google.com/drive/download/) and sign in, or use *Backup → Sign in with Google* instead. The backup is optional.
4. Your games start syncing automatically. Check *Games → Found on this PC* for anything else, such as folders Syncer didn't recognise.

Syncer isn't code-signed yet, so Windows SmartScreen may warn the first time: click *More info → Run anyway*. Each release lists the SHA-256 of its files, and the built-in updater checks downloads against it. You only install once; after that Syncer updates itself (you can turn that off in Settings).

### Linking a second PC

1. Install Syncer on the second PC.
2. On one PC, open **Devices** and paste the other PC's ID.
3. Accept the request that appears on the other PC.

Every synced game then shows up on the other PC at the right local path, even if the user name or Documents location differs.

## How it works

Syncer is a small desktop app built on two tools that already work well:

- **Syncthing between your PCs.** Syncthing copies saves straight from PC to PC, end-to-end encrypted, with no account and no cloud storage in between. Your PCs don't need to be on the same network: when they can't reach each other directly, Syncthing's public relays pass the encrypted data along. Syncer installs and drives your own Syncthing; it doesn't bundle it.
- **Google Drive as the backup.** Syncer copies your saves into `My Drive\GameSaveBackup`. Only changed files are copied, and replaced or deleted files are kept as restore points for 30 days by default. Google Drive for desktop uploads the folder, or Syncer signs in to Google itself.

```
 PC A                                  PC B
 ┌─────────────┐   Syncthing (P2P)   ┌─────────────┐
 │ save folders│◄───────────────────►│ save folders│
 │ syncer-meta │◄───────────────────►│ syncer-meta │  ← each PC's folder list, portable paths
 └──────┬──────┘                     └─────────────┘
        │ Syncer.exe --background (Task Scheduler)
        ▼
 G:\My Drive\GameSaveBackup\<game>\…      ← mirror
 G:\My Drive\GameSaveBackup\.versions\…   ← replaced/deleted files, thinned, pruned after N days
        │ Google Drive for desktop (or Syncer's own sign-in)
        ▼
   Google Drive
```

Syncthing only syncs while both PCs are on. The backup covers the rest: when the PC you played on last is switched off, Syncer can take the newer saves from its backup, but only when nothing on this PC can get lost.

Save locations come from the [Ludusavi manifest](https://github.com/mtkennerly/ludusavi-manifest), which is built from [PCGamingWiki](https://www.pcgamingwiki.com). A scheduled task (`\Syncer-Background`) runs the sync and backup at logon and every few hours, also when the window is closed.

All features, from Steam Cloud detection to separate saves per person and syncing Vortex mods, are described in [docs/features.md](docs/features.md). Launchers can use Syncer through a local API: [docs/api.md](docs/api.md).

## Privacy

Syncer has no server of its own, and the developer receives no data from it: no account, no telemetry, no crash reports.

- Your saves go directly between your PCs (Syncthing) and into your own Google Drive.
- *Sign in with Google* asks for one permission, `drive.file`: access only to the files Syncer creates. It can't see anything else in your Drive. The sign-in token stays on your PC, encrypted for your Windows account, and *Sign out* revokes it at Google.
- Syncthing's public discovery servers see your PCs' device IDs and IP addresses, as with any Syncthing setup. GitHub (game list, update check) and Google (time check) see your IP address, as with any web request.
- Settings and logs stay on your PC in `%APPDATA%\Syncer` and `%LOCALAPPDATA%\Syncer`.

The full policy is in [PRIVACY.md](PRIVACY.md).

## FAQ

**Can this get me banned by anti-cheat?**
Syncing saves only touches save files, the same files the game writes itself. Syncer doesn't change the game's own files and doesn't inject anything into it. The one exception is the experimental *Sync deployed mods in the game folder* option (off by default), which copies mod files, including DLLs, into a game's folder: don't use it for games with anti-cheat. Saves of online games usually live on the game's servers anyway, so there's nothing for Syncer to sync.

**Which games does it support?**
About 14,000 games whose save locations are listed on PCGamingWiki, through the Ludusavi manifest, from any store: Steam, Epic, GOG, EA, Ubisoft, Xbox, or no store at all. Games that aren't listed can be added by hand with any folder. If a game's save location is missing or wrong, fixing it on PCGamingWiki fixes it for everyone; Syncer refreshes the list every week. You can also [request a game](https://github.com/ApolloF/syncer/issues/new?template=game_request.yml).

**Why not just use Steam Cloud?**
Use it where it works: Syncer leaves folders that Steam Cloud really keeps to Steam, and checks this per folder rather than trusting the store page. Syncer is for everything else: games from other stores or no store, games without cloud saves, mod saves Steam skips (such as SKSE co-saves), and a history of restore points Steam Cloud doesn't keep.

**What happens when two PCs changed the same save?**
Syncer doesn't guess. The game shows **2 versions**, and you pick which one to keep, file by file or all of one PC's files at once. The other version goes into the backup history instead of being deleted.

**How do I restore an older save?**
On the Games page, click the history button (*Restore from backup*) next to the game. Pick the latest backup or any earlier restore point. Your current files are saved as a restore point first, so a restore can be undone.

**Is my Google Drive safe?**
Syncer can only see the files and folders it created itself (`GameSaveBackup`); Google enforces this through the `drive.file` permission. The backup goes straight from your PC to your Drive. You can revoke access at any time with *Sign out* or at [myaccount.google.com/permissions](https://myaccount.google.com/permissions).

**Do I need Google Drive?**
No. Syncthing works on its own between PCs that are on at the same time. The backup adds restore points and lets a PC pick up saves from a PC that is switched off.

**How is this different from Ludusavi?**
[Ludusavi](https://github.com/mtkennerly/ludusavi) is a backup tool: you back up and restore saves, on one or more PCs. Syncer keeps saves in sync between PCs continuously, in the background, and keeps a backup with history next to that. Syncer uses Ludusavi's manifest to find saves.

**How do I stop using it?**
*Settings → Undo everything* stops all syncing on this PC and can also unlink your PCs, stop or uninstall Syncthing, and delete the backups. Then uninstall Syncer from *Windows Settings → Apps*. Your save files are never deleted.

## Limitations

- **Windows 10 and 11 only (64-bit).** No macOS, Linux or Steam Deck version.
- **Not code-signed yet**, so SmartScreen warns on the first run.
- **Google Drive is the only backup target.** Other clouds, NAS or WebDAV aren't supported.
- **Syncthing needs both PCs on at the same time.** Saves from a PC that is off arrive through the backup only when that PC's backup has fully reached Google Drive.
- **Saves stored in the Windows registry aren't synced**, only save files and folders.
- **One PC at a time.** Changes from your other PCs wait while a game runs. Playing the same game on two PCs at once ends in *2 versions* for you to sort out.
- **English only.**
- **Mod sync is experimental**, and only works with Vortex.

## Report a problem

[Open an issue](https://github.com/ApolloF/syncer/issues/new/choose). The activity log is under *Settings → Activity log*, and the full log file is `%APPDATA%\Syncer\syncer.log`. Logs can contain game names, folder paths and your Google account's email address, so check them before you post. See [CONTRIBUTING.md](CONTRIBUTING.md) if you'd like to help.

## Develop

Requirements: Go 1.26+, Node 22+, [Wails v2](https://wails.io) (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`).

```bash
wails dev          # live-reloading app
go test ./...      # backend tests
wails build        # build/bin/Syncer.exe
```

Release builds that should offer *Sign in with Google* need a Google OAuth client: in the [Google Cloud console](https://console.cloud.google.com), create a project, enable the Google Drive API, set up the OAuth consent screen (External, scope `.../auth/drive.file` only, then **publish** it; in Testing, sign-ins expire after 7 days) and create an OAuth client of type *Desktop app*. Put its id and secret in the repository secrets `GDRIVE_CLIENT_ID` and `GDRIVE_CLIENT_SECRET`; CI passes them in with `-ldflags "-X main.gdriveClientID=… -X main.gdriveClientSecret=…"`. (A desktop app's client secret isn't confidential.)

The backend lives in `internal/`: `syncthing` (REST client), `meta` (cross-PC folder sharing), `discover` (manifest, scan, installed games), `backup` (mirror/versions/restore), `gdrive` (Google sign-in and Drive sync), `tasks` (Task Scheduler), `paths` (portable paths, sync safety rules) and `winx` (Windows priority, full-screen and foreground checks). The Svelte 5 frontend is in `frontend/src`. Draft winget and Scoop manifests are in [packaging/](packaging/).

## License

Syncer is free software: you can redistribute it and/or modify it under the terms of the [GNU Affero General Public License v3.0](LICENSE). It comes with no warranty. The third-party code it includes is listed, with its licenses, in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

Game save locations come from the [Ludusavi manifest](https://github.com/mtkennerly/ludusavi-manifest), which is built from [PCGamingWiki](https://www.pcgamingwiki.com) (CC BY-NC-SA 3.0). Syncer downloads it at runtime and doesn't include it. Syncing is done by [Syncthing](https://syncthing.net) (MPL-2.0), installed separately.

Syncer is not affiliated with Valve, Google, the Syncthing Foundation or Nexus Mods. Syncthing is a trademark of the Syncthing Foundation; Google Drive is a trademark of Google LLC.
