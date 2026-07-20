package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/schollz/progressbar/v3"

	"github.com/ernado/peertube"
)

// globalPruneFlags holds the top-level "prune" command flags.
type globalPruneFlags struct {
	maxSize        string
	keepPerChannel int
	concurrency    int
	yes            bool
}

// sizeCollectConcurrency is the default number of in-flight GET /videos/{id}
// requests when measuring storage; sizes are not exposed by the listings, so
// one request per video is unavoidable.
const sizeCollectConcurrency = 8

// pruneAll measures the storage used across every channel of the authenticated
// user and deletes videos — oldest first, always from the channel currently
// occupying the most bytes — until the total fits within --max-size. It is a
// dry run unless --yes is given.
func (o *options) pruneAll(ctx context.Context, outw, logw io.Writer, p globalPruneFlags) error {
	if err := o.validateAuth(); err != nil {
		return err
	}
	if p.maxSize == "" {
		return errors.New("missing required flag: --max-size")
	}
	target, err := parseSize(p.maxSize)
	if err != nil {
		return err
	}
	if p.keepPerChannel < 0 {
		return errors.New("--keep-per-channel must not be negative")
	}
	if p.concurrency <= 0 {
		p.concurrency = sizeCollectConcurrency
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

	videos, err := collectSizedVideos(ctx, client, channels, p.concurrency, logw)
	if err != nil {
		return err
	}
	total := peertube.TotalSize(videos)
	fmt.Fprintf(logw, "%d videos across %d channels using %s (target %s).\n",
		len(videos), len(channels), formatSize(total), formatSize(target))
	if total <= target {
		fmt.Fprintln(logw, "Already within the target; nothing to prune.")
		return nil
	}

	prune := peertube.SelectToFit(videos, target, p.keepPerChannel)
	if len(prune) == 0 {
		fmt.Fprintf(logw, "Nothing prunable: every video is protected by --keep-per-channel=%d.\n", p.keepPerChannel)
		return nil
	}

	var freed int64
	tw := tabwriter.NewWriter(outw, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CHANNEL\tPUBLISHED\tSIZE\tUUID\tNAME")
	for i := range prune {
		v := &prune[i]
		freed += v.Size
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			v.Channel, v.PublishedAt.Format("2006-01-02"), formatSize(v.Size), v.UUID, v.Name)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	remaining := total - freed
	fmt.Fprintf(logw, "Selected %d videos freeing %s, leaving %s.\n",
		len(prune), formatSize(freed), formatSize(remaining))
	if remaining > target {
		fmt.Fprintf(logw, "Warning: still %s over target — the rest is protected by --keep-per-channel=%d.\n",
			formatSize(remaining-target), p.keepPerChannel)
	}
	if !p.yes {
		fmt.Fprintf(logw, "Dry run: re-run with --yes to delete these %d videos.\n", len(prune))
		return nil
	}

	items := make([]deletable, 0, len(prune))
	for i := range prune {
		v := &prune[i]
		items = append(items, deletable{ID: v.ID, UUID: v.UUID, Name: v.Name, Size: v.Size})
	}
	deleted, errs := deleteVideos(ctx, client, items, logw)
	for _, err := range errs {
		fmt.Fprintf(logw, "  failed to %v\n", err)
	}
	fmt.Fprintf(logw, "Deleted %d/%d videos, freeing %s (now %s).\n",
		len(items)-len(errs), len(items), formatSize(deleted), formatSize(total-deleted))
	if len(errs) > 0 {
		return fmt.Errorf("%d of %d deletions failed", len(errs), len(items))
	}
	return nil
}

// collectSizedVideos lists every channel's videos and annotates each with its
// storage size, fetched with at most concurrency requests in flight.
func collectSizedVideos(
	ctx context.Context,
	client *peertube.Client,
	channels []peertube.Channel,
	concurrency int,
	logw io.Writer,
) ([]peertube.SizedVideo, error) {
	var all []peertube.SizedVideo
	for _, ch := range channels {
		videos, err := client.ChannelVideos(ctx, ch.Name)
		if err != nil {
			return nil, fmt.Errorf("list videos of %s: %w", ch.Name, err)
		}
		for _, v := range videos {
			if v.IsLive {
				continue // live videos have no settled stored size
			}
			all = append(all, peertube.SizedVideo{Video: v, Channel: ch.Name})
		}
	}
	if len(all) == 0 {
		return nil, nil
	}

	// One request per video, so show progress: this is the slow part.
	bar := newItemBar(len(all), "measuring", logw)
	var (
		wg   sync.WaitGroup
		sem  = make(chan struct{}, concurrency)
		mu   sync.Mutex
		errs []error
	)
	for i := range all {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			defer func() { _ = bar.Add(1) }() // ProgressBar.Add is goroutine-safe
			size, err := client.VideoSize(ctx, all[i].ID)
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("size of %s (%q): %w", all[i].UUID, all[i].Name, err))
				mu.Unlock()
				return
			}
			all[i].Size = size
		}(i)
	}
	wg.Wait()
	_ = bar.Finish()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A video whose size could not be read counts as zero, which would make it
	// look free to keep. Report rather than silently under-count.
	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintf(logw, "  warning: %v\n", err)
		}
		return nil, fmt.Errorf("could not measure %d of %d videos; refusing to prune on incomplete sizes", len(errs), len(all))
	}
	// Stable order so equal-size ties and output are deterministic.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Channel != all[j].Channel {
			return all[i].Channel < all[j].Channel
		}
		return all[i].PublishedAt.After(all[j].PublishedAt)
	})
	return all, nil
}

// newItemBar builds an item-oriented progress bar rendering to w. It counts
// videos processed, not bytes, unlike the upload bar.
func newItemBar(count int, description string, w io.Writer) *progressbar.ProgressBar {
	return progressbar.NewOptions(count,
		progressbar.OptionSetWriter(w),
		progressbar.OptionSetDescription(description),
		progressbar.OptionShowCount(),
		progressbar.OptionSetWidth(30),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionClearOnFinish(),
	)
}

// deletable identifies a video to delete, independent of how it was selected,
// so both prune commands share one deletion loop.
type deletable struct {
	ID   int
	UUID string
	Name string
	Size int64 // bytes reclaimed on success; zero when the size is unknown
}

// deleteVideos deletes each video in turn, advancing a progress bar, and
// returns the bytes freed plus one error per failed deletion. Errors are
// returned rather than printed so the caller can report them after the bar has
// finished, where a redraw cannot overwrite them. A canceled context stops the
// loop instead of failing every remaining video.
func deleteVideos(
	ctx context.Context,
	client *peertube.Client,
	videos []deletable,
	logw io.Writer,
) (int64, []error) {
	bar := newItemBar(len(videos), "deleting", logw)
	defer func() { _ = bar.Finish() }()

	var (
		freed int64
		errs  []error
	)
	for i := range videos {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			return freed, errs
		}
		v := &videos[i]
		if err := client.DeleteVideo(ctx, v.ID); err != nil {
			errs = append(errs, fmt.Errorf("delete %s (%q): %w", v.UUID, v.Name, err))
		} else {
			freed += v.Size
		}
		_ = bar.Add(1)
	}
	return freed, errs
}

// sizeUnits maps human size suffixes to byte multipliers, longest suffix first
// so "gb" is matched before a bare "b" would be.
var sizeUnits = []struct {
	suffix string
	mult   int64
}{
	{"tib", 1 << 40}, {"gib", 1 << 30}, {"mib", 1 << 20}, {"kib", 1 << 10},
	{"tb", 1 << 40}, {"gb", 1 << 30}, {"mb", 1 << 20}, {"kb", 1 << 10},
	{"t", 1 << 40}, {"g", 1 << 30}, {"m", 1 << 20}, {"k", 1 << 10},
	{"b", 1},
}

// parseSize parses a storage size like "100gb", "1.5t", "512mib" or a bare byte
// count. Units are binary (1gb = 1024 mb), matching how PeerTube reports quota.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("empty size")
	}
	invalid := fmt.Errorf("invalid size %q (use e.g. 100gb, 1.5tb, 512mb)", s)
	for _, u := range sizeUnits {
		num, ok := strings.CutSuffix(s, u.suffix)
		if !ok {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
		if err != nil {
			return 0, invalid
		}
		if n < 0 {
			return 0, fmt.Errorf("size %q must not be negative", s)
		}
		return int64(n * float64(u.mult)), nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, invalid
	}
	if n < 0 {
		return 0, fmt.Errorf("size %q must not be negative", s)
	}
	return n, nil
}

// formatSize renders a byte count in binary units, e.g. "1.5 GB".
func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
