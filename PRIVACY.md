# Privacy

Syncer runs on your PCs. It has no server of its own and collects nothing about you.

## Google sign-in

If you choose *Backup → Sign in with Google*, Syncer asks Google for one permission, `drive.file`: access only to the files Syncer creates in your Google Drive (the `GameSaveBackup` folder). It can't see, read or change anything else in your Drive.

- Syncer uses this access only to upload your game save backups to that folder and download them to your other PCs.
- The sign-in token is stored on your PC in `%APPDATA%\Syncer\gdrive.token`, encrypted with Windows' data protection for your Windows account. It is never sent anywhere except to Google.
- Syncer reads your Google account's email address only to show which account is signed in.
- *Sign out* deletes the token and revokes Syncer's access at Google. You can also remove access at any time at https://myaccount.google.com/permissions.

Syncer's use of information received from Google APIs follows the [Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy), including its Limited Use requirements.

## Everything else

- Saves are synced directly between your PCs by [Syncthing](https://syncthing.net), end-to-end encrypted. When your PCs can't reach each other directly, Syncthing's public relays pass the encrypted data along without being able to read it.
- With Google Drive for desktop, Syncer only writes files into your Drive folder on this PC; Google Drive for desktop does the upload.
- Syncer downloads the [Ludusavi manifest](https://github.com/mtkennerly/ludusavi-manifest) from GitHub to recognise games, and checks GitHub once a day for a new Syncer release (you can turn that off in Settings). Before deleting expired restore points from the backup, Syncer checks the time with Google (an empty request to google.com), so a wrong PC clock can't delete them early. These requests send nothing about you or your games.
- Logs and settings stay on your PC in `%APPDATA%\Syncer`.

Questions: open an issue at https://github.com/ApolloF/syncer/issues.
