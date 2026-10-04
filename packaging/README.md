# Packaging (drafts, not submitted)

Manifests for Windows package managers. Nothing here is submitted anywhere; the steps below are for the maintainer.

The release workflow (`.github/workflows/build.yml`) attaches two files to every `v*` tag:

| Asset | Used by |
|---|---|
| `Syncer-amd64-installer.exe` (NSIS, per-user, no admin prompt) | winget |
| `Syncer.exe` (portable) | Scoop |

GitHub publishes a SHA-256 digest for each asset, which `render-manifests.ps1` reads.

## winget

`winget/` holds templates for the three manifest files (`version`, `installer`, `defaultLocale`, schema 1.10.0). `{{VERSION}}`, `{{INSTALLER_SHA256}}` and `{{RELEASE_DATE}}` are filled in per release.

- Package id `ApolloF.Syncer`, installer type `nullsoft`, scope `user`.
- `ProductCode` `ApolloFSyncer` is the installer's uninstall key (`HKCU\...\Uninstall\ApolloFSyncer`), so winget recognises existing installs. The installer and the self-updater both write `DisplayVersion`, so `winget upgrade` sees the right version after Syncer updated itself.

First submission, after the release is published:

```powershell
./packaging/render-manifests.ps1 -Version 1.0.0
winget validate --manifest packaging/out/manifests/a/ApolloF/Syncer/1.0.0
winget install --manifest packaging/out/manifests/a/ApolloF/Syncer/1.0.0   # test install (needs: winget settings --enable LocalManifestFiles)
```

Then fork `microsoft/winget-pkgs`, copy the folder to `manifests/a/ApolloF/Syncer/1.0.0/` and open a pull request (or run `wingetcreate submit packaging/out/manifests/a/ApolloF/Syncer/1.0.0`). Later versions: `wingetcreate update ApolloF.Syncer --version <v> --urls https://github.com/ApolloF/syncer/releases/download/v<v>/Syncer-amd64-installer.exe --submit`.

Unsigned installers are accepted, but SmartScreen still warns until the builds are code-signed.

## Scoop

`scoop/syncer.json` installs the portable `Syncer.exe` and adds a Start menu shortcut. `checkver` follows GitHub releases, and `autoupdate` downloads the new exe and computes its hash. `render-manifests.ps1` also updates its version and hash.

To publish, create a bucket repository (for example `ApolloF/scoop-bucket`), put `syncer.json` in its `bucket/` folder, and tell users:

```powershell
scoop bucket add apollof https://github.com/ApolloF/scoop-bucket
scoop install apollof/syncer
```

Before announcing it, test one thing: Syncer's background task points at the path Syncer was started from. Check that it points at `scoop\apps\syncer\current\Syncer.exe` (which survives updates) and not at a versioned folder.

Syncer's own updater replaces `Syncer.exe` in place, which Scoop doesn't know about. The manifest's `notes` tell Scoop users to turn off *Install updates automatically*.
