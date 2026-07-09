# peertube

A small, focused Go client for **uploading videos** to a [PeerTube](https://joinpeertube.org)
instance, plus a matching CLI. Built against the PeerTube 8.1 OpenAPI spec
(`openapi.json`).

It deliberately implements only what's needed to publish a video:

- OAuth2 password-grant login (`/api/v1/users/token`).
- Legacy single-request upload (`POST /api/v1/videos/upload`).
- Resumable chunked upload (`POST/PUT /api/v1/videos/upload-resumable`),
  following the [node-uploadx](https://github.com/kukhariev/node-uploadx/blob/master/proto.md)
  protocol PeerTube uses.

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
peertube login --url https://peertube.example.org -U alice -p secret

# Then commands work without repeating credentials.
peertube channel list
peertube --file video.mp4 --name "My video" --tags go,peertube
```

Or pass everything inline (no login required):

```bash
peertube \
  --url https://peertube.example.org \
  --username alice --password secret \
  --file video.mp4 --name "My video"
```

Commands:

| Command | Purpose |
|---------|---------|
| `peertube [flags]` | Upload a video (root command). |
| `peertube login` | Verify and persist credentials; `--default` sets the default instance. |
| `peertube channel list` | List the authenticated user's channels. |

Credential resolution, highest precedence first:

1. `--url` / `--username` / `--password` flags.
2. `PEERTUBE_USER` / `PEERTUBE_PASSWORD` environment variables.
3. Saved config (`login`) — `os.UserConfigDir()/peertube/config.json`, written
   with `0600` since it holds the password. The default instance supplies `--url`
   when omitted.

Other notes:

- **Channel auto-discovery**: omit `--channel-id` and the CLI picks your channel
  automatically when the account has exactly one; with several it lists them and
  asks you to choose.
- Uploads are resumable by default (with a progress bar); pass `--legacy` for a
  single request.

Run `peertube --help` or `peertube <command> --help` for all flags.

## License

See [LICENSE](LICENSE).
