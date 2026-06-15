# discord-cli

`discord-cli` is a heavily modified version of Discordo focused on listening/output workflows.
It listens to a target Discord channel and writes formatted message output to stdout so you can pipe it into downstream tooling.

## What this fork is for

- Listen-only workflow for a target channel via `--channel`
- Stream output to other processes (for example, another parser/automation script)
- Optional keyword filtering with `--filter`
- Historical backtesting mode with `--hours`
- QR-based auth + keyring token storage

## Build

```bash
go build -o discord-cli .
```

## Usage

Authenticate first (stores token in keyring):

```sh
./discord-cli
```

Stream live messages from a channel:

```sh
./discord-cli --channel 123456789012345678
```

Filter to only matching messages:

```sh
./discord-cli --channel 123456789012345678 --filter "buy,sell,TSLA"
```

Backtest the last N hours (historical fetch mode):

```sh
./discord-cli --channel 123456789012345678 --hours 24
```

You can also provide a token with `--token` or with `DISCORD_CLI_TOKEN`.

## Configuration

Config path:

- Unix: `$XDG_CONFIG_HOME/discord-cli/config.toml` or `$HOME/.config/discord-cli/config.toml`
- Darwin: `$HOME/Library/Application Support/discord-cli/config.toml`
- Windows: `%AppData%/discord-cli/config.toml`

### Presence

By default the client connects with an **invisible** presence so it does not
appear online while listening. To use a normal online presence instead, set in
`config.toml`:

```toml
status = "default"   # online; other values: "idle", "dnd", "invisible"
```

## Keyring token entry

If you need to manually set a token:

### Windows

```sh
cmdkey /add:discord-cli /user:token /pass:YOUR_DISCORD_TOKEN
```

### macOS

```sh
security add-generic-password -s discord-cli -a token -w "YOUR_DISCORD_TOKEN"
```

### Linux

```sh
secret-tool store --label="Discord Token" service discord-cli username token
```

## Changelog

See [CHANGELOG.md](CHANGELOG.md) for release notes.
