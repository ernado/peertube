# AGENTS.md

Guidance for agents working in this repository.

## What this is

A small, focused Go client for **uploading videos to PeerTube** plus a cobra CLI.
It is intentionally narrow: authenticate, manage channels, upload videos. It is
**not** a full PeerTube API client. Derived from `openapi.json` (PeerTube 8.1),
which is committed for reference but not code-generated from — the client is
hand-written.

Module: `github.com/ernado/peertube` (Go 1.26).

## Layout

Library (package `peertube`, repo root):

| File | Responsibility |
|------|----------------|
| `client.go` | `Client`, `NewClient`, options, `Doer` interface, `apiURL` |
| `auth.go` | OAuth2 login (password grant), refresh, 2FA/OTP |
| `channels.go` | `MyChannels`, `CreateChannel`, avatar/banner uploads |
| `upload.go` | Legacy single-request multipart upload |
| `upload_resumable.go` | Resumable chunked upload (node-uploadx protocol) |
| `params.go` | `UploadParams` validation + form/JSON serialization |
| `videos.go` | `ChannelVideos`, `DeleteVideo`, per-channel `SelectPrunable` |
| `prune.go` | `VideoSize`, `SizedVideo`, size-budget `SelectToFit` (global prune) |
| `types.go` | Enums (`Privacy`, `CommentsPolicy`), shared types |
| `errors.go` | `APIError` (HTTP status + PeerTube error code) |
| `doc.go` | Package doc |

CLI (package `main`, `cmd/peertube/`):

| File | Responsibility |
|------|----------------|
| `main.go` | cobra command tree, flag wiring, `options`, validation |
| `run.go` | Command implementations (upload, channel ops, login) |
| `prune.go` | Global `prune` command: size collection, `parseSize`/`formatSize` |
| `config.go` | Persisted credentials + cached OAuth token (`os.UserConfigDir()/peertube/config.json`) |
| `prompt.go` | Interactive username/password prompts (hidden input via `x/term`) |

Commands: `peertube upload`, `login`, `prune`,
`channel list|create|set-avatar|set-banner|prune|remove`.

## Conventions

- **Errors**: use `github.com/go-faster/errors` in the library (`errors.Wrap`,
  `errors.Errorf`, `errors.New`). Remember `errors.Wrap(nil, ...)` returns
  non-nil — only wrap inside a non-nil check. The CLI package uses stdlib
  `fmt.Errorf` with `%w` (it's the app boundary, not the reusable library).
- **Testability**: the `Client` depends on the `Doer` interface, not
  `*http.Client`. Tests hit `httptest.Server` or a stub `Doer` — never the
  network. CLI commands write through `cmd.OutOrStdout()` / `cmd.ErrOrStderr()`
  and read prompts from `cmd.InOrStdin()` so they are fully testable.
- **Streaming**: uploads stream the file body via `io.Pipe` — never buffer whole
  files in memory. Preserve this when touching upload code.
- **Non-2xx** responses become `*APIError` via `newAPIError(resp)`; callers use
  `errors.As`.
- **Optional fields**: pointer bools (`*bool`) distinguish unset from explicit
  false; zero-valued scalars are omitted from requests.
- **Auth reuse**: `(*options).login` prefers the cached access token, then the
  refresh grant, then the password grant, caching whatever it obtains. Anything
  that must verify credentials (like the `login` command) sets `o.relogin`
  first, or it will silently accept a token from an earlier session.
- Match the surrounding style; keep comments at the existing density.

## Commands

```bash
go build ./...
go test ./...              # all tests
go test -race -cover ./... # what to run before committing
go vet ./...
gofmt -w .                 # or gofmt -l . to check
```

Always run `gofmt -w .`, `go vet ./...`, and `go test -race ./...` before
committing. Keep the two-package split intact: the library must not import the
CLI, and prefer keeping the library dependency-light (currently only
`go-faster/errors`).

## Testing notes

- CLI tests use a `TestMain` that redirects `configPathFn` to a temp dir, so no
  test touches the user's real `config.json`. Per-test isolation via
  `withTempConfig`.
- `execViaCmd` / `execViaCmdStdin` build a fresh command tree and capture output.
- `mockServer` in `cmd/peertube/main_test.go` fakes the PeerTube endpoints
  (auth, users/me, channel create, avatar/banner, resumable upload).

## Gotchas

- `boolPtr` triggers a Go 1.26 `new(expr)` lint hint — intentionally kept for
  readability; not a bug.
- Resumable chunk size must be a multiple of 1024 (node-uploadx requirement).
- Channel handles may contain `@`; URL-escape them in paths (`url.PathEscape`).
