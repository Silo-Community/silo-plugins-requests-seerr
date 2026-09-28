package seerr

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Download phases, as the host names them (DownloadProgress.phase). Seerr
// drops the queue's trackedDownloadState, so this plugin never reports
// import_blocked.
const (
	PhaseQueued      = "queued"
	PhaseDownloading = "downloading"
	PhasePaused      = "paused"
	PhaseStalled     = "stalled"
	PhaseImporting   = "importing"
)

// phaseRank is the host's aggregation order: when downloads differ, the
// highest-ranked phase wins (stalled > downloading > importing > paused >
// queued).
var phaseRank = map[string]int{
	PhaseQueued:      0,
	PhasePaused:      1,
	PhaseImporting:   2,
	PhaseDownloading: 3,
	PhaseStalled:     4,
}

// MapDownloadPhase maps a queue item's *arr status onto a phase. Without
// trackedDownloadState a finished download reads as importing even when its
// import is stuck.
func MapDownloadPhase(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "paused":
		return PhasePaused
	case "warning":
		return PhaseStalled
	case "downloading":
		return PhaseDownloading
	case "completed":
		return PhaseImporting
	default:
		return PhaseQueued
	}
}

// Progress is one Seerr request's download progress across its downloads.
type Progress struct {
	Phase string
	// BytesTotal and BytesLeft are 0 when any download does not know its
	// size yet: summing only the known sizes would overstate the percentage.
	BytesTotal int64
	BytesLeft  int64
	// EstimatedCompletion is the latest estimate; nil when no download has one.
	EstimatedCompletion *time.Time
	Downloads           int
}

// EvaluateProgress aggregates a request's download list. Items that belong to
// one download count once (see downloadKey), so a season pack's size is not
// multiplied by its episode count. Returns nil for an empty list.
func EvaluateProgress(items []DownloadItem) *Progress {
	if len(items) == 0 {
		return nil
	}
	p := &Progress{Phase: PhaseQueued}
	seen := make(map[string]bool, len(items))
	sizeUnknown := false
	for i, item := range items {
		if phase := MapDownloadPhase(item.Status); phaseRank[phase] > phaseRank[p.Phase] {
			p.Phase = phase
		}
		if eta, ok := item.eta(); ok && (p.EstimatedCompletion == nil || eta.After(*p.EstimatedCompletion)) {
			p.EstimatedCompletion = &eta
		}
		key := downloadKey(item, i)
		if seen[key] {
			continue
		}
		seen[key] = true
		p.Downloads++
		total := roundBytes(item.Size)
		if total <= 0 {
			sizeUnknown = true
			continue
		}
		p.BytesTotal += total
		p.BytesLeft += min(max(roundBytes(item.SizeLeft), 0), total)
	}
	if sizeUnknown {
		p.BytesTotal, p.BytesLeft = 0, 0
	}
	return p
}

// downloadKey identifies the download an item belongs to: its downloadId, else
// the release's title and size, else its place in the list. A release Sonarr
// holds back (a delay profile, an unavailable download client) has no
// downloadId yet, and Sonarr lists it once per episode, each item with the
// same title and size.
func downloadKey(item DownloadItem, index int) string {
	if id := strings.TrimSpace(item.DownloadID); id != "" {
		return "download:" + id
	}
	if title := strings.TrimSpace(item.Title); title != "" {
		return "pending:" + title + "|" + strconv.FormatInt(roundBytes(item.Size), 10)
	}
	return "index:" + strconv.Itoa(index)
}

func roundBytes(n float64) int64 {
	return int64(math.Round(n))
}

// eta parses the item's estimated completion. The Unix epoch is what Seerr
// makes of a null estimate, so it counts as unknown.
func (d DownloadItem) eta() (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(d.EstimatedCompletionTime))
	if err != nil || !t.After(time.Unix(0, 0)) {
		return time.Time{}, false
	}
	return t, true
}
