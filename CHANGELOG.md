# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.1] - 2026-07-02

### Added

- **Automatic share-token generation.** When the server starts without
  `CLAUDE_SHARE_TOKEN`, it now generates a strong, Bitwarden-style passphrase
  (6 words from the EFF large wordlist, ~77 bits of entropy) instead of
  refusing to start. The token is chosen with `crypto/rand` and printed once at
  startup for the operator to copy to each client.
- **Framed startup banner.** The generated token is rendered inside a centered,
  color-highlighted box (ANSI on a TTY, plain text when piped; honors
  `NO_COLOR`) so it stands out in the console.
- **`-version` flag** on both `claude-share-server` and `claude-share-client`,
  backed by a single in-repo version source that release builds stamp with the
  git tag.

### Changed

- Setting `CLAUDE_SHARE_TOKEN` explicitly still takes precedence; generation
  only happens when it is unset. No cryptographic changes — a generated
  passphrase flows through the existing HKDF-BLAKE2s → Noise PSK derivation
  unchanged.
- Documentation (README and website) updated to describe the auto-generated
  token flow.

## [0.1.0] - Initial release

### Added

- Share one machine's authenticated Claude Code installation with clients on an
  internal network over an end-to-end encrypted WebSocket channel.
- Noise `NNpsk0_25519_ChaChaPoly_BLAKE2s` secure channel: ephemeral X25519 keys
  (forward secrecy) plus a pre-shared key derived from the shared token (mutual
  authentication).
- Per-client isolated workspace and dedicated `claude` subprocess.
- Content-addressed project sync, hardened file I/O, and loopback-by-default
  binding (`--allow-public` required for non-loopback addresses).
- Cross-platform release binaries (Linux, macOS, Windows) built by GitHub
  Actions.

[0.1.1]: https://github.com/dmux/claude-share/releases/tag/v0.1.1
[0.1.0]: https://github.com/dmux/claude-share/releases/tag/v0.1.0
