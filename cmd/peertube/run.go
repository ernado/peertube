package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ernado/peertube"
	"github.com/schollz/progressbar/v3"
)

// execute logs in and uploads the video. Progress goes to logw (stderr) and the
// final result to outw (stdout); both are injected so the flow is testable.
func (o options) execute(ctx context.Context, outw, logw io.Writer) error {
	f, err := os.Open(o.file)
	if err != nil {
		return fmt.Errorf("open video: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat video: %w", err)
	}

	client, err := o.login(ctx, logw)
	if err != nil {
		return err
	}

	channelID, err := o.resolveChannelID(ctx, client, logw)
	if err != nil {
		return err
	}

	name := o.name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(o.file), filepath.Ext(o.file))
	}
	params := peertube.UploadParams{
		Name:            name,
		ChannelID:       channelID,
		Privacy:         peertube.Privacy(o.privacy),
		Category:        o.category,
		Licence:         o.licence,
		Language:        o.language,
		Description:     o.description,
		Support:         o.support,
		Tags:            o.tags,
		NSFW:            boolPtr(o.nsfw),
		WaitTranscoding: boolPtr(o.waitTranscoding),
		DownloadEnabled: boolPtr(o.downloadEnabled),
	}
	filename := filepath.Base(o.file)

	fmt.Fprintf(logw, "Uploading %q (%d bytes) to channel %d...\n", name, info.Size(), channelID)

	// Advance a progress bar as the library reads the file for upload.
	bar := newUploadBar(info.Size(), logw)
	reader := progressbar.NewReader(f, bar)

	var res *peertube.UploadedVideo
	if o.legacy {
		res, err = client.Upload(ctx, params, filename, &reader)
	} else {
		res, err = client.UploadResumable(ctx, params, filename, &reader, info.Size(),
			peertube.ResumableOptions{ChunkSize: o.chunkSize})
	}
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	_ = bar.Finish()

	fmt.Fprintf(outw, "Uploaded: id=%d uuid=%s shortUUID=%s\n", res.ID, res.UUID, res.ShortUUID)
	return nil
}

// newUploadBar builds a byte-oriented progress bar rendering to w.
func newUploadBar(size int64, w io.Writer) *progressbar.ProgressBar {
	return progressbar.NewOptions64(size,
		progressbar.OptionSetWriter(w),
		progressbar.OptionSetDescription("uploading"),
		progressbar.OptionShowBytes(true),
		progressbar.OptionShowCount(),
		progressbar.OptionSetWidth(30),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionClearOnFinish(),
	)
}

// login builds an authenticated client from the shared auth flags.
func (o options) login(ctx context.Context, logw io.Writer) (*peertube.Client, error) {
	client, err := peertube.NewClient(o.url)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(logw, "Logging in to %s as %s...\n", o.url, o.username)
	if _, err := client.Login(ctx, o.username, o.password, peertube.LoginOptions{OTP: o.otp}); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	return client, nil
}

// loginAndSave verifies the credentials against the instance and, on success,
// persists them to the config file for reuse by other commands. Any username or
// password still missing after flags/env/config is prompted for interactively.
func (o options) loginAndSave(ctx context.Context, in io.Reader, logw io.Writer, makeDefault bool) error {
	if o.url == "" {
		return fmt.Errorf("missing required flag: --url")
	}

	p := newPrompter(in, logw)
	if o.username == "" {
		u, err := p.line("PeerTube username: ")
		if err != nil {
			return fmt.Errorf("read username: %w", err)
		}
		o.username = strings.TrimSpace(u)
	}
	if o.password == "" {
		pw, err := p.password("PeerTube password: ")
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		o.password = pw
	}

	if err := o.validateAuth(); err != nil {
		return err
	}
	if _, err := o.login(ctx, logw); err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cfg.set(o.url, instance{Username: o.username, Password: o.password}, makeDefault)
	if err := cfg.save(); err != nil {
		return err
	}

	path, _ := configPathFn()
	fmt.Fprintf(logw, "Saved credentials for %s to %s\n", o.url, path)
	if cfg.Default == o.url {
		fmt.Fprintf(logw, "%s is now the default instance\n", o.url)
	}
	return nil
}

// listChannels prints the authenticated user's video channels to outw.
func (o options) listChannels(ctx context.Context, outw, logw io.Writer) error {
	if err := o.validateAuth(); err != nil {
		return err
	}
	client, err := o.login(ctx, logw)
	if err != nil {
		return err
	}
	channels, err := client.MyChannels(ctx)
	if err != nil {
		return fmt.Errorf("list channels: %w", err)
	}
	if len(channels) == 0 {
		fmt.Fprintln(outw, "No video channels.")
		return nil
	}

	tw := tabwriter.NewWriter(outw, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tDISPLAY NAME")
	for _, ch := range channels {
		fmt.Fprintf(tw, "%d\t%s\t%s\n", ch.ID, ch.Name, ch.DisplayName)
	}
	return tw.Flush()
}

// channelCreateFlags holds the "channel create" command flags.
type channelCreateFlags struct {
	name        string
	displayName string
	description string
	support     string
	avatar      string // optional image file
	banner      string // optional image file
}

// createChannel creates a new video channel and prints its id, optionally
// uploading an avatar and/or banner afterwards.
func (o options) createChannel(ctx context.Context, outw, logw io.Writer, p channelCreateFlags) error {
	if err := o.validateAuth(); err != nil {
		return err
	}
	if p.name == "" || p.displayName == "" {
		var missing []string
		if p.name == "" {
			missing = append(missing, "--name")
		}
		if p.displayName == "" {
			missing = append(missing, "--display-name")
		}
		return fmt.Errorf("missing required flags: %s", strings.Join(missing, ", "))
	}

	client, err := o.login(ctx, logw)
	if err != nil {
		return err
	}
	ch, err := client.CreateChannel(ctx, peertube.CreateChannelParams{
		Name:        p.name,
		DisplayName: p.displayName,
		Description: p.description,
		Support:     p.support,
	})
	if err != nil {
		return fmt.Errorf("create channel: %w", err)
	}
	fmt.Fprintf(outw, "Created channel: id=%d name=%s\n", ch.ID, ch.Name)

	if p.avatar != "" {
		if err := uploadChannelImage(ctx, outw, client, "avatar", ch.Name, p.avatar); err != nil {
			return err
		}
	}
	if p.banner != "" {
		if err := uploadChannelImage(ctx, outw, client, "banner", ch.Name, p.banner); err != nil {
			return err
		}
	}
	return nil
}

// channelImageFlags holds the "channel set-avatar"/"set-banner" command flags.
type channelImageFlags struct {
	handle string
	file   string
}

// setChannelImage uploads an avatar or banner for an existing channel.
func (o options) setChannelImage(ctx context.Context, outw, logw io.Writer, kind string, p channelImageFlags) error {
	if err := o.validateAuth(); err != nil {
		return err
	}
	if p.handle == "" || p.file == "" {
		var missing []string
		if p.handle == "" {
			missing = append(missing, "--channel")
		}
		if p.file == "" {
			missing = append(missing, "--file")
		}
		return fmt.Errorf("missing required flags: %s", strings.Join(missing, ", "))
	}

	client, err := o.login(ctx, logw)
	if err != nil {
		return err
	}
	return uploadChannelImage(ctx, outw, client, kind, p.handle, p.file)
}

// uploadChannelImage opens the image file and uploads it as the channel's
// avatar or banner (kind is "avatar" or "banner").
func uploadChannelImage(ctx context.Context, outw io.Writer, client *peertube.Client, kind, handle, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", kind, err)
	}
	defer func() { _ = f.Close() }()
	filename := filepath.Base(path)

	var imgs []peertube.ActorImage
	switch kind {
	case "avatar":
		imgs, err = client.SetChannelAvatar(ctx, handle, filename, f)
	case "banner":
		imgs, err = client.SetChannelBanner(ctx, handle, filename, f)
	default:
		return fmt.Errorf("unknown image kind %q", kind)
	}
	if err != nil {
		return fmt.Errorf("set %s: %w", kind, err)
	}

	if len(imgs) > 0 && imgs[0].FileURL != "" {
		fmt.Fprintf(outw, "Set %s for %s: %s\n", kind, handle, imgs[0].FileURL)
	} else {
		fmt.Fprintf(outw, "Set %s for %s\n", kind, handle)
	}
	return nil
}

// resolveChannelID returns the channel to upload to. When --channel-id is set it
// is used as-is; otherwise the user's channels are fetched: a single channel is
// selected automatically, while multiple channels require an explicit choice.
func (o options) resolveChannelID(ctx context.Context, client *peertube.Client, logw io.Writer) (int, error) {
	if o.channelID != 0 {
		return o.channelID, nil
	}

	channels, err := client.MyChannels(ctx)
	if err != nil {
		return 0, fmt.Errorf("discover channels: %w", err)
	}
	switch len(channels) {
	case 0:
		return 0, fmt.Errorf("account has no video channels; create one first")
	case 1:
		ch := channels[0]
		fmt.Fprintf(logw, "Auto-selected channel %d (%s)\n", ch.ID, channelLabel(ch))
		return ch.ID, nil
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "account has %d channels; select one with --channel-id:\n", len(channels))
		for _, ch := range channels {
			fmt.Fprintf(&b, "  %d\t%s\n", ch.ID, channelLabel(ch))
		}
		return 0, fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
	}
}

func channelLabel(ch peertube.Channel) string {
	if ch.DisplayName != "" && ch.DisplayName != ch.Name {
		return fmt.Sprintf("%s — %s", ch.Name, ch.DisplayName)
	}
	return ch.Name
}

func boolPtr(b bool) *bool { return &b }
