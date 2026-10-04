# Privacy

Last updated: 4 October 2026

Syncer is a free Windows app that keeps your PC game saves in sync between your own PCs and backs them up to your own Google Drive. It runs on your PCs and has no server of its own. The maintainer receives no data from the app: no account, no telemetry, no crash reports.

## Who is responsible

Syncer is made and published by Florian, who publishes as ApolloF, in the Netherlands. Contact: [privacy@apollof.nl](mailto:privacy@apollof.nl). Because the app never sends your data to him, he doesn't hold or process it; the data stays on your PCs, in your Google Drive and with the services listed below. If you email him, he is the controller of that email (see [Your rights](#your-rights)).

## Google sign-in and your Google data

Signing in with Google is optional: Syncer can also back up through Google Drive for desktop, or not at all. If you choose *Backup → Sign in with Google*, Syncer asks Google for one permission, `drive.file`: access only to the files and folders Syncer itself creates in your Google Drive (the `GameSaveBackup` folder). It can't see, read or change anything else in your Drive.

- **What Syncer accesses:** the backup files it created, and your Google account's email address (read from Google Drive's account information).
- **How it uses them:** only to upload your game save backups to that folder, download them to your other PCs, and show which account is signed in. The email address also lets Syncer notice when you switch to a different Google account, so one account's backup is never uploaded into another's.
- **Where it is stored:** on your PC only. The sign-in token is stored in `%APPDATA%\Syncer\gdrive.token`, encrypted with Windows' data protection for your Windows account. The email address is kept with the token, in Syncer's local sync state and in its local log. A working copy of the backup is kept in `%LOCALAPPDATA%\Syncer\GoogleDrive`.
- **Who it is shared with:** nobody. The token and your files go only between your PC and Google. The maintainer never receives them, and no person reads them.
- **Revoking access:** *Sign out* deletes the token and revokes Syncer's access at Google. You can also remove access at any time at https://myaccount.google.com/permissions.

Syncer's use and transfer to any other app of information received from Google APIs will adhere to the [Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy), including the Limited Use requirements. Data from Google APIs is not sold, not used for advertising, not used to determine credit-worthiness or for lending, and not used to develop, improve or train AI or machine learning models.

## Syncing between your PCs

Saves are synced directly between your PCs by [Syncthing](https://syncthing.net), end-to-end encrypted. Syncthing is a separate program with its own settings. To let your PCs find each other, it uses the Syncthing project's public discovery servers, which see each PC's device ID and IP address. When your PCs can't reach each other directly, Syncthing's public relays pass the encrypted data along without being able to read it. See [Syncthing's security notes](https://docs.syncthing.net/users/security.html).

If you let Syncer install Syncthing, it runs Windows Package Manager (`winget`), which downloads Syncthing from Microsoft's winget catalog and Syncthing's official release.

## Other network requests

- With Google Drive for desktop, Syncer only writes files into your Drive folder on this PC; Google Drive for desktop does the upload, under Google's terms.
- Syncer downloads the [Ludusavi manifest](https://github.com/mtkennerly/ludusavi-manifest) from GitHub to recognise games.
- It checks GitHub once a day for a new Syncer release and downloads it from GitHub. You can turn the check off in Settings.
- Before deleting expired restore points, Syncer checks the time with Google (an empty request to google.com), so a wrong PC clock can't delete them early.

These requests send nothing about you or your games. GitHub and Google see your IP address, as with any web request.

## What stays on your PC

Settings, sync state and logs stay in `%APPDATA%\Syncer` and `%LOCALAPPDATA%\Syncer`. Logs can contain game names, folder paths and your Google account's email address. They leave your PC only if you send them to someone yourself, for example with a bug report.

## Third parties Syncer contacts

- Google (Drive API, sign-in, time check): [Google Privacy Policy](https://policies.google.com/privacy)
- Syncthing discovery servers and relays: [syncthing.net](https://syncthing.net/)
- GitHub (game manifest, updates): [GitHub Privacy Statement](https://docs.github.com/en/site-policy/privacy-policies/github-general-privacy-statement)
- Microsoft (winget, only when installing Syncthing): [Microsoft Privacy Statement](https://privacy.microsoft.com/privacystatement)

## No selling, no sharing

Your data is never sold, rented, shared with advertisers or data brokers, or used for advertising. There are no ads, analytics or trackers in Syncer.

## How long data is kept

Everything Syncer stores stays on your PCs and in your Drive until you delete it. Older versions in the backup are kept for the number of days you set (30 by default; restore points Syncer makes to protect your saves for a year) and then removed. The maintainer keeps nothing, because he receives nothing.

## Deleting your data

- *Sign out* on the Backup page revokes Google access and deletes the token.
- *Settings → Undo everything* stops syncing and can unlink your PCs and delete the backups. Your save files are never deleted.
- Uninstall Syncer from *Settings → Apps* in Windows. Delete `My Drive\GameSaveBackup` in Google Drive to remove the backups.

## Your rights

Under the GDPR you can ask for access to, correction or deletion of personal data about you, restrict or object to its use, and get a copy of it. Since Syncer sends nothing to the maintainer, the only personal data he may hold is an email you send him; it is used only to answer you and deleted when it is no longer needed. Write to [privacy@apollof.nl](mailto:privacy@apollof.nl). You can also complain to the Dutch data protection authority, the [Autoriteit Persoonsgegevens](https://autoriteitpersoonsgegevens.nl/), or the authority where you live.

## Children

Syncer isn't directed at children under 16 and doesn't knowingly collect data from anyone. Google's own age rules apply to Google accounts.

## Changes

If this policy changes, the new version is published here with a new date at the top.

## Contact

Questions about privacy: [privacy@apollof.nl](mailto:privacy@apollof.nl). Bugs and feature requests: https://github.com/ApolloF/syncer/issues.
