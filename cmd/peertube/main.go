// Command peertube is a minimal CLI to log in to a PeerTube instance and upload
// videos, built on the github.com/ernado/peertube library.
//
// Usage:
//
//	peertube login  --url https://peertube.example.org -U alice
//	peertube upload --file video.mp4 --name "My video"
//	peertube channel list
//
// The username and password may be supplied via flags, the PEERTUBE_USER and
// PEERTUBE_PASSWORD environment variables, or a prior "login" (which persists
// them). The login command prompts interactively for any it is still missing.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

type options struct {
	url      string
	username string
	password string
	otp      string

	file      string
	name      string
	channelID int

	privacy     int
	category    int
	licence     int
	language    string
	description string
	support     string
	tags        []string
	nsfw        bool

	waitTranscoding bool
	downloadEnabled bool

	legacy    bool
	chunkSize int64
}

// newRootCmd builds the cobra command tree. It is a function (not a package var)
// so tests can build a fresh command with isolated flag state.
//
// The root command is a parent that holds the shared authentication flags as
// persistent flags; all work happens in subcommands (upload, login, channel).
func newRootCmd() *cobra.Command {
	var o options

	cmd := &cobra.Command{
		Use:          "peertube",
		Short:        "Upload videos to a PeerTube instance",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		// Applies to every subcommand: resolve credentials from flags, then
		// environment, then the saved config.
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			o.resolveCredentials()
			return nil
		},
	}

	// Shared authentication flags, available to all subcommands.
	pf := cmd.PersistentFlags()
	pf.StringVarP(&o.url, "url", "u", "", "PeerTube instance URL (required)")
	pf.StringVarP(&o.username, "username", "U", "", "account username (or set PEERTUBE_USER)")
	pf.StringVarP(&o.password, "password", "p", "", "account password (or set PEERTUBE_PASSWORD)")
	pf.StringVar(&o.otp, "otp", "", "two-factor authentication code, if enabled")

	cmd.AddCommand(newUploadCmd(&o))
	cmd.AddCommand(newLoginCmd(&o))
	cmd.AddCommand(newChannelCmd(&o))

	return cmd
}

// newUploadCmd builds the "upload" command.
func newUploadCmd(o *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "upload",
		Short:        "Upload a video",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.validate(); err != nil {
				return err
			}
			return o.execute(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}

	f := cmd.Flags()
	f.StringVarP(&o.file, "file", "f", "", "path to the video file (required)")
	f.StringVarP(&o.name, "name", "n", "", "video name (defaults to the file name)")
	f.IntVarP(&o.channelID, "channel-id", "C", 0, "target channel id (auto-discovered if unset)")

	f.IntVarP(&o.privacy, "privacy", "P", 1, "privacy: 1=public 2=unlisted 3=private 4=internal 5=password")
	f.IntVar(&o.category, "category", 0, "category id (0 = unset)")
	f.IntVar(&o.licence, "licence", 0, "licence id (0 = unset)")
	f.StringVarP(&o.language, "language", "L", "", "language ISO 639 code, e.g. en")
	f.StringVarP(&o.description, "description", "d", "", "video description")
	f.StringVarP(&o.support, "support", "s", "", "support text")
	f.StringSliceVarP(&o.tags, "tags", "t", nil, "video tags (repeatable or comma-separated, max 5)")
	f.BoolVarP(&o.nsfw, "nsfw", "N", false, "mark the video as NSFW")

	f.BoolVar(&o.waitTranscoding, "wait-transcoding", true, "wait for transcoding before publishing")
	f.BoolVar(&o.downloadEnabled, "download", true, "allow downloading the video")

	f.BoolVar(&o.legacy, "legacy", false, "use the single-request upload instead of resumable")
	f.Int64Var(&o.chunkSize, "chunk-size", 5<<20, "resumable chunk size in bytes (multiple of 1024)")

	return cmd
}

// newLoginCmd builds the "login" command, which verifies and persists
// credentials for a PeerTube instance, prompting for any missing interactively.
func newLoginCmd(o *options) *cobra.Command {
	var makeDefault bool
	cmd := &cobra.Command{
		Use:          "login",
		Short:        "Verify and save credentials for a PeerTube instance",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return o.loginAndSave(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr(), makeDefault)
		},
	}
	cmd.Flags().BoolVar(&makeDefault, "default", false, "set this instance as the default for other commands")
	return cmd
}

// newChannelCmd builds the "channel" command group.
func newChannelCmd(o *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channel",
		Short: "Inspect video channels",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(&cobra.Command{
		Use:          "list",
		Short:        "List the authenticated user's video channels",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return o.listChannels(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	})
	return cmd
}

// missingAuth returns the required auth flags that are unset.
func (o options) missingAuth() []string {
	var missing []string
	if o.url == "" {
		missing = append(missing, "--url")
	}
	if o.username == "" {
		missing = append(missing, "--username (or PEERTUBE_USER)")
	}
	if o.password == "" {
		missing = append(missing, "--password (or PEERTUBE_PASSWORD)")
	}
	return missing
}

// validateAuth checks the flags needed to authenticate.
func (o options) validateAuth() error {
	if missing := o.missingAuth(); len(missing) > 0 {
		return fmt.Errorf("missing required flags: %s", strings.Join(missing, ", "))
	}
	return nil
}

// validate checks the flags needed to upload a video.
func (o options) validate() error {
	missing := o.missingAuth()
	if o.file == "" {
		missing = append(missing, "--file")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required flags: %s", strings.Join(missing, ", "))
	}
	return nil
}
