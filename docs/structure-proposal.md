# Proposal: move root `package main` files into `internal/`

Status: proposal only. Nothing has been moved.

## Today

The root holds ~11k lines of Go in 29 non-test files, all `package main`. `internal/*` already has 16 clean, Wails-free packages (`syncthing`, `backup`, `discover`, `mods`, …). The root mixes three kinds of code:

1. **Wails glue**: methods on `*App` that the frontend calls (`app.go`, most of `folders.go`, `accounts.go`, `tray.go`, `update.go`, …). 144 `func (a *App)` methods.
2. **Feature logic as free functions** that take a `*syncthing.Client` and `store.Settings` and don't need `App`: the deployed-mods state machine (`deployed.go`, 1771 lines, ~40 free funcs: `sourceTick`, `receiverTick`, `planUpdate`, `letSync`, …), `vortexshare.go`, `folderissue.go`, `origins.go`, `decisions.go`, `launcherrepair.go`.
3. **Entry points**: `main.go`, `background.go` (scheduled runs), `api.go` (launcher API over a named pipe, `--api`).

Costs: every root test compiles and links the whole app; package-level state (`historyMu`, `autoAddMu`, `taskSeen`) is reachable from everything; boundaries between features exist only by file name, so `deployed.go` can (and does) call helpers from any other file.

## Proposal

Keep `package main` as thin glue: `main.go`, `app.go`, `tray.go` and the `*App` methods that translate between the UI and a feature package. Move feature logic down, one package per feature, starting with the parts that already don't need `App`:

| New package | From | Why first / notes |
|---|---|---|
| `internal/deployed` | `deployed.go` (free funcs), `modfolders.go` helpers | Largest file, already mostly free functions; has its own tests and an e2e test. `App` keeps `ModUpdatePreview`, `ApplyModUpdate`, … as wrappers. |
| `internal/vortexshare` | `vortexshare.go` | Self-contained; depends on `meta`, `mods`, `syncthing`. |
| `internal/folderissue` | `folderissue.go`, `launcherrepair.go` | Pure diagnosis + repair; only needs `syncthing`, `meta`, `backup`. |
| `internal/api` | `api.go` | Launcher API server; needs a small interface over the operations it calls instead of `*App`. Documented in `docs/api.md`. |
| `internal/bg` | `background.go`, `autoadd.go`, `cloudpull.go` | Scheduled run; currently shares helpers with the UI path, so do it after the above. |
| `internal/protect` | `protect.go`, `decisions.go`, `undo.go` | Restore points and undo; touches `backup`, `meta`, `conflict`. |

View types bound to the frontend (`main.FolderView`, `main.Overview`, …) stay in `package main`, so `frontend/wailsjs/go/models.ts` keeps its `main.*` namespace and the frontend doesn't change.

## How

- One package per PR, behaviour-neutral: move code, export only what `main` calls, run the gate (`go test ./...`, `npm run check`, `npx vitest run`, `wails build`). No logic changes mixed in.
- Package-level state moves with its feature and stays unexported (`historyMu` → `internal/accounts` or the history package; `autoAddMu` → `internal/bg`).
- Where a moved function needs something from `App` (notifications, `refresh` events), pass a small interface or callback rather than importing `main`.
- Tests move with their code; `deployed_e2e_test.go` becomes `internal/deployed` and stops needing the whole app to compile.

## Risks

- Large diffs on files with active work (`deployed.go`, `folders.go`, `accounts.go`): merge conflicts with open branches. Do each move when its file has no open PR.
- Exporting names exposes internals more than today; mitigated by `internal/` (not importable outside the module) and keeping the exported surface to what `main` uses.
- Wails binding generation only looks at bound structs; moving methods off `*App` by accident would drop them from `App.d.ts`. `npm run check` catches that, since the frontend would fail to typecheck.

## Not proposed

- Splitting `App` itself or changing the frontend API.
- A `cmd/` layout: Wails v2 expects `main` at the project root next to `wails.json`.
