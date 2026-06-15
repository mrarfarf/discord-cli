# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.0] - 2026-06-14

Reliability and correctness pass aimed at long-running listeners and bulk
`--hours` backfills, plus a quieter default presence.

### Changed
- **Default presence is now `invisible` instead of `online`.** The listener no
  longer announces itself as online. Presence is coerced to invisible at three
  layers (config default, config-load fallback, and the presence-set site) so it
  cannot accidentally go online. Set `status = "default"` in `config.toml` to opt
  back into a normal online presence.
- Log file is opened in **append** mode (was truncate) so logs survive across
  runs, and created with `0644` permissions (was `0777`). Config and cache
  directory permissions tightened to `0755`.

### Fixed
- **Unbounded memory growth on long-running listeners.** The seen-message dedup
  set is now a bounded FIFO (10,000 entries) instead of a map that grew forever.
- **Crash on gateway reconnect in `--hours` mode.** The one-shot historical fetch
  is guarded with `sync.Once`, preventing a double-close panic when `onReady`
  fires again on resume.
- **Duplicate counting in the `--hours` history fetch.** Reworked the pagination
  loop to collect each in-window message exactly once and removed the `goto`; the
  reported message count is now accurate.
- **Concurrent websocket writes during QR login.** The heartbeat goroutine and
  main loop now serialize all writes behind a mutex, preventing frame corruption
  and panics.
- **Connection leak on compressed responses.** Brotli-decoded response bodies now
  close the underlying network connection so it can be reused.
- **Fragile authentication-error detection.** Auth failures are now matched by
  type (REST `401` / gateway close `4004`) instead of substring matching on the
  error string.
- **Silent failures.** The process now exits non-zero on error so a supervisor
  can detect a dead instance, and an invalid `--log-level` is rejected instead of
  silently defaulting to `info`.

### Removed
- The API client no longer mutates the global `http.DefaultClient`; it uses a
  dedicated transport.
- Removed accidentally-committed `.DS_Store` files from the repository.

## [1.0.0]

- Initial release: listen-only Discord channel client with keyword filtering
  (`--filter`), historical backfill (`--hours`), QR-based authentication, and
  keyring token storage.

[1.1.0]: https://github.com/mrarfarf/discord-cli/compare/v.1.0.0...v1.1.0
[1.0.0]: https://github.com/mrarfarf/discord-cli/releases/tag/v.1.0.0
