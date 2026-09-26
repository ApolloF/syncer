# Syncer launcher API

A local API for game launchers (such as [WaterLauncher](https://github.com/ApolloF/WaterLauncher)): show a game's save status, sync saves before a game starts, back them up after it exits, and deal with conflicts.

## Connecting

- **Transport:** the named pipe `\\.\pipe\syncer`. Only the current Windows user can connect (the pipe's DACL grants access to that user alone), and remote clients are refused.
- **Who serves it:** the Syncer window, while it runs (including in the tray). Otherwise start `Syncer.exe --api`: it serves the same pipe without a window and exits after a minute without connections (after any backup it started has finished). When the pipe is already served, `--api` exits right away.
- **Finding Syncer.exe:** the `DisplayIcon` value of `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\ApolloFSyncer`, or `%LOCALAPPDATA%\Programs\Syncer\Syncer.exe`.
- **Clients should check the server:** `GetNamedPipeServerProcessId`, then compare the owner of that process with the current user, so another account can't pose as Syncer.

## Messages

[JSON-RPC 2.0](https://www.jsonrpc.org/specification), one JSON object per line (UTF-8, `\n`-terminated, at most 1 MB). Requests on one connection may overlap; answers carry the request's `id`. A request without `id` is a notification and gets no answer.

```json
{"jsonrpc":"2.0","id":1,"method":"gameStatus","params":{"title":"Elden Ring"}}
{"jsonrpc":"2.0","id":1,"result":{"known":true,"folders":[{"id":"elden-ring","label":"Elden Ring","state":"idle", …}]}}
```

Errors use the standard codes (`-32700` parse error, `-32600` invalid request, `-32601` unknown method, `-32602` bad params) and `-32000` for a valid call that didn't work, with the reason in `message`.

## Versioning

`status` returns `protocol` (now **1**). New methods and fields can be added within a version; clients ignore fields they don't know. A change that breaks clients raises the version. Clients should check it and treat an unknown or missing Syncer as "not available".

## Methods

| Method | Params | Result |
|---|---|---|
| `status` | – | `{protocol, version, window, syncthing, paused, pausedUntil?, backingUp, lastBackup?, games, conflicts}` |
| `games` | – | every save folder, as *Folder* objects |
| `gameStatus` | `{title?, dir?, steamAppId?, gogId?}` (at least one of the first three) | `{known, folders: [Folder]}` |
| `syncNow` | `{ids: [folder id], timeoutMs?}` (default 30 s, at most 2 min) | `[{id, done, state, needBytes}]` |
| `backupNow` | `{wait?, timeoutMs?}` (default 2 min, at most 30 min) | `{started, finished, ok, at?, copied, errors?}` |
| `conflicts` | `{id}` | `[{rel, copy, device, deviceName, size, modified, missing, copySize, copyModified}]` |
| `resolveConflict` | `{id, copy, useCopy}` | `true` |
| `open` | – | `true` (shows the Syncer window, starting it if needed) |
| `registerGames` | `{games: [{title, dir?, steamAppId?, gogId?}]}` (at most 20,000) | `{games: n}` |
| `subscribe` | – | `true`, then `changed` notifications on this connection |

**Folder:** `{id, label, path, sync, backup, state, needBytes, errors, conflicts, exists, modified?, backedUp?, newerOn?, newerAt?}`.

- `sync`: synced with other PCs; `false` means only backed up.
- `state`: Syncthing's state (`idle`, `scanning`, `syncing`, …), or `backup-only`, `off`, `paused`.
- `newerOn`: another PC backed up a newer save that hasn't arrived here yet.

### Notes

- **gameStatus** matches folders by the game's title, the names the game database (Ludusavi manifest) has for its Steam app or install folder, and the title a launcher registered for that install folder. Emulator save folders (`Game (RUNE saves)`) count as their game. `known: false` means Syncer has no saves for it.
- **syncNow** asks Syncthing to rescan each synced folder and waits until the folder is idle with nothing left to fetch. `done: false` after the timeout means it's still catching up (for example, the other PC is offline). Backup-only folders report `done: true, state: "not-synced"`; paused folders report `state: "paused"`.
- **backupNow** runs the same incremental backup as *Back up now*. With `wait`, it returns once the backup has finished or the timeout has passed (`finished: false`).
- **resolveConflict** keeps one version: with `useCopy` the conflict copy replaces the file, otherwise the current file stays. The other version goes to backup history, never deleted.
- **registerGames** replaces the previous list. Syncer keeps it in `%APPDATA%\Syncer\launcher-games.json`.
- **subscribe:** Syncer checks folder states every 3 seconds while someone is subscribed and sends `{"jsonrpc":"2.0","method":"changed","params":{}}` when something changed. Call `games` or `gameStatus` again for the details.
