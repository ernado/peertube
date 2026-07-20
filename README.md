# peertube

A small, focused Go client for **uploading videos** to a [PeerTube](https://joinpeertube.org)
instance, plus a matching CLI. Built against the PeerTube 8.1 OpenAPI spec
(`openapi.json`).

It deliberately implements only what's needed to publish a video and manage the
channel it lives in:

- OAuth2 password-grant login, token refresh, and 2FA/OTP (`/api/v1/users/token`).
- Legacy single-request upload (`POST /api/v1/videos/upload`).
- Resumable chunked upload (`POST/PUT /api/v1/videos/upload-resumable`),
  following the [node-uploadx](https://github.com/kukhariev/node-uploadx/blob/master/proto.md)
  protocol PeerTube uses.
- Channel management: list, create, and set avatar/banner images.

```bash
go get github.com/ernado/peertube
```

## Library

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/ernado/peertube"
)

func main() {
	ctx := context.Background()

	c, err := peertube.NewClient("https://peertube.example.org")
	if err != nil {
		log.Fatal(err)
	}
	if _, err := c.Login(ctx, "alice", "secret"); err != nil {
		log.Fatal(err)
	}

	f, err := os.Open("video.mp4")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	info, _ := f.Stat()

	// Resumable upload: survives transient failures, good for large files.
	res, err := c.UploadResumable(ctx, peertube.UploadParams{
		Name:      "My video",
		ChannelID: 3,
		Privacy:   peertube.PrivacyPublic,
		Tags:      []string{"go", "peertube"},
	}, "video.mp4", f, info.Size())
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("uploaded: uuid=%s", res.UUID)
}
```

For small files there is also `Upload` (single multipart request):

```go
res, err := c.Upload(ctx, params, "video.mp4", f)
```

### Channels

```go
// Discover channels (useful to find a ChannelID for uploads).
channels, err := c.MyChannels(ctx)

// Create a channel.
ch, err := c.CreateChannel(ctx, peertube.CreateChannelParams{
	Name:        "my_channel", // immutable handle
	DisplayName: "My Channel",
})

// Set its avatar / banner (PNG or JPEG).
avatar, _ := os.Open("avatar.png")
defer avatar.Close()
_, err = c.SetChannelAvatar(ctx, ch.Name, "avatar.png", avatar)
```

### Testability

The client talks to any `Doer` (`Do(*http.Request) (*http.Response, error)`),
which `*http.Client` satisfies. Inject a stub or an `httptest.Server` to test
without touching the network:

```go
c, _ := peertube.NewClient("https://x", peertube.WithHTTPClient(myDoer))
```

Non-2xx responses are returned as `*peertube.APIError` carrying the HTTP status
and PeerTube error `code` (e.g. `quota_reached`, `invalid_grant`):

```go
var apiErr *peertube.APIError
if errors.As(err, &apiErr) && apiErr.Code == "quota_reached" {
	// ...
}
```

## CLI

The CLI is built with [cobra](https://github.com/spf13/cobra) and shows an upload
progress bar via [schollz/progressbar](https://github.com/schollz/progressbar).

```bash
go install github.com/ernado/peertube/cmd/peertube@latest

# Save credentials once (verified against the instance, stored in the config).
# Prompts for username/password if not passed via flags or environment.
peertube login --url https://peertube.example.org

# Then commands work without repeating credentials.
peertube channel list
peertube upload --file video.mp4 --name "My video" --tags go,peertube
```

Or pass everything inline (no login required):

```bash
peertube upload \
  --url https://peertube.example.org \
  --username alice --password secret \
  --file video.mp4 --name "My video"
```

Commands:

| Command | Purpose |
|---------|---------|
| `peertube upload` | Upload a video. |
| `peertube login` | Verify and persist credentials; prompts for missing username/password; `--default` sets the default instance. |
| `peertube channel list` | List the authenticated user's channels. |
| `peertube channel create` | Create a video channel (`--name`, `--display-name`, optional `--avatar`/`--banner`). |
| `peertube channel set-avatar` / `set-banner` | Upload an avatar/banner image (`--channel`, `--file`). |
| `peertube channel prune` | Delete old videos from a channel by age and/or count (dry-run unless `--yes`). |
| `peertube channel remove <handle>` | Delete a whole channel and its videos (dry-run unless `--yes`). |
| `peertube prune` | Delete videos across **all** channels until storage fits `--max-size` (dry-run unless `--yes`). |

### Pruning to a storage budget

`peertube prune` is the global, size-driven counterpart to `channel prune`: it
measures every video in all of your channels and deletes until the total fits
within `--max-size` (`100gb`, `1.5tb`, `512mb`, or a bare byte count; units are
binary).

Channels are **balanced against each other** — each deletion takes the oldest
video from whichever channel currently occupies the most bytes. Large channels
shrink first and small ones are left alone, instead of any single channel being
emptied. `--keep-per-channel N` protects the newest N videos of every channel;
if that protection makes the budget unreachable, the command reports the
shortfall rather than deleting protected videos.

```bash
# Preview what it takes to get under 100 GB.
peertube prune --max-size 100gb

# Enforce it, but never leave a channel with fewer than 5 videos.
peertube prune --max-size 100gb --keep-per-channel 5 --yes
```

Sizes are not exposed by PeerTube's listing endpoints, so this issues one
`GET /videos/{id}` per video (`--concurrency`, default 8, bounds the parallel
lookups). If any size cannot be read the command refuses to prune rather than
treat that video as occupying zero bytes.

### Pruning a single channel

`channel prune` deletes videos from a channel by two combinable criteria:

- `--older-than <age>` — delete videos published before the cutoff. Age accepts
  `30d`, `2w`, `6mo`, `1y`, or any Go duration like `48h` (`mo`/`y` are
  approximate: 30/365 days).
- `--keep-last <N>` — always keep the newest N videos, delete the rest.

With both, the newest N are kept and, among the rest, those older than the age
are deleted. It's a **dry run by default** (lists what would be deleted); pass
`--yes` to actually delete.

```bash
# Preview: keep the 10 newest, drop the rest.
peertube channel prune -c my_channel --keep-last 10

# Delete everything older than 6 months, but always keep at least 5.
peertube channel prune -c my_channel --older-than 6mo --keep-last 5 --yes
```

Credential resolution, highest precedence first:

1. `--url` / `--username` / `--password` flags.
2. `PEERTUBE_USER` / `PEERTUBE_PASSWORD` environment variables.
3. Saved config (`login`) — `os.UserConfigDir()/peertube/config.json`, written
   with `0600` since it holds the password. The default instance supplies `--url`
   when omitted.
4. `login` additionally prompts on the terminal for any username/password still
   missing (password input is hidden).

### Token caching

Commands do not re-run the password grant every time. The OAuth token pair is
cached in the same config file and reused in this order:

1. The cached access token, while it is still valid — no auth request at all.
2. Otherwise the refresh token, exchanged for a new pair (the password is never
   sent).
3. Otherwise a full login.

Each new token is cached in turn. Tokens are treated as expired a minute early
so one does not die mid-request, and a token the instance reports no lifetime
for is not cached at all.

A token revoked server-side still looks valid locally, so it surfaces as a
`401` on the request itself; `--relogin` ignores the cache and logs in again.
`login` always verifies the credentials it saves, cache or not.

Other notes:

- **Channel auto-discovery**: omit `--channel-id` and the CLI picks your channel
  automatically when the account has exactly one; with several it lists them and
  asks you to choose.
- Uploads are resumable by default (with a progress bar); pass `--legacy` for a
  single request.

Run `peertube --help` or `peertube <command> --help` for all flags.

## License

See [LICENSE](LICENSE).
