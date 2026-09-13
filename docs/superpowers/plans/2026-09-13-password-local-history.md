# Passwords, local copy and history implementation plan

User-approved changes: store encrypted passwords in TOML; treat unnamed cp paths as local; record every copy in SQLite, search and reuse history.

- [x] Add credentials.Store with AES-256-GCM enc:v1 envelope, connection-name AAD and independent 0600 master.key. Encrypt may create key atomically; decrypt never creates it. Test tampering, key errors and concurrent initialization.
- [x] Add new/edit --password hidden prompt, edit --clear-password and global --key-file. Use encrypted passwords for connect/c/cp and noninteractive completion. Plaintext TOML remains rejected. new retains save-only behavior; it does not require the host online.
- [x] Generalize transfer.Copy with nil SFTP endpoints as local filesystem and ParseEndpoint for bare local paths. Preserve atomic staging, no-clobber and force behavior, error reporting and cancellation. Test all four local/remote directions.
- [x] Add history.Store with SQLite begin/finish/get/search. Record source, destination, cwd, force, config/key/known_hosts paths, times, bytes, status and error; never passwords or key material. Persist running before transfer; finish with separate bounded context after cancellation. An unrecordable attempt fails before file transfer.
- [x] Add --history-file, ah history [query] --limit N, ah history show ID, ah history run ID. Fuzzy query matches case-insensitive keyword substrings; show emits safely quoted command, run uses structured arguments without shell evaluation and appends a new history entry. Replay resolves local paths against stored cwd and current saved connection definitions; it uses saved config/key/known-hosts paths unless explicitly overridden.
- [x] Add local path candidates to both cp argument positions while retaining alias: remote candidates. Keep existing Bash/Zsh quote handling and noninteractive remote timeout.
- [x] Verify encrypted-password CLI integration, local copy/history search/replay/failures/cancellation, real shell Tab and full build/vet/race. Update README and project contract. Preserve prior uncommitted c alias and Makefile; do not commit/push without a new request.

Validation: make check; go test -race ./...; CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build; expanded Bash/Zsh PTY completion suite (12 cases) passed normal and race runs. Built bin/ah and exercised local copy, search, show and replay from a different working directory with a temporary SQLite database.
