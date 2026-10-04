# Contributing to Syncer

Thanks for helping. Syncer is a one-person project, so small, focused changes get reviewed fastest.

## Reporting problems

- **Bugs:** use the [bug report](https://github.com/ApolloF/syncer/issues/new?template=bug_report.yml) form and include the part of `%APPDATA%\Syncer\syncer.log` around the problem. Remove anything personal first (paths with your user name, your email address).
- **A game isn't found:** use the [game form](https://github.com/ApolloF/syncer/issues/new?template=game_request.yml). Save locations come from PCGamingWiki through the Ludusavi manifest, so fixing the game's PCGamingWiki page helps every tool that uses it.
- **Security issues:** don't open a public issue. Email me@apollof.nl instead.

## Code changes

1. Open an issue first for anything larger than a small fix, so we can agree on the approach before you spend time on it.
2. Fork, create a branch from `main`, and keep the change to one topic.
3. Before you open the pull request, run:

   ```bash
   go vet ./...
   go test ./...
   cd frontend && npm ci && npm run check && npx vitest run
   cd .. && wails build
   ```

4. Add or update tests for behaviour you change (`*_test.go` next to the Go code, `*.test.ts` next to frontend helpers in `frontend/src/lib`).
5. Try the change in the real app with `wails dev`. Anything that touches syncing, backups or restores must never delete or overwrite a save without a restore point.

Requirements and the project layout are in the [README](README.md#develop).

## Style

- Follow the existing structure and naming; don't add a new library when the standard library or an existing dependency covers it.
- UI text is short and plain: say what happens, in the words a player uses.
- No telemetry, analytics or network calls beyond those listed in [PRIVACY.md](PRIVACY.md). A change that adds one needs the privacy policy updated in the same pull request.

## License

Syncer is licensed under the [AGPL-3.0](LICENSE). By sending a pull request you agree that your contribution is licensed under the same license.
