package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// doctorSync is the running daemon's tracker sync status as `itervox doctor`
// reports it (#117), read from the "## Sync" section of a live
// .itervox/HEARTBEAT.md. Lag is computed at doctor's own clock.
type doctorSync struct {
	heartbeatSync
	Lag time.Duration // zero when nothing was pulled yet
}

// collectDoctorSync reads the sync status from the heartbeat at hbPath and
// returns it with the lag warning, empty when the lag is within the
// threshold. nil when the heartbeat has no sync section.
func collectDoctorSync(hbPath string, now time.Time) (*doctorSync, string) {
	data, err := os.ReadFile(hbPath)
	if err != nil {
		return nil, ""
	}
	hs, ok := parseHeartbeatSync(string(data))
	if !ok {
		return nil, ""
	}
	ds := &doctorSync{heartbeatSync: hs}
	warnAfter := hs.WarnAfter
	if warnAfter <= 0 {
		warnAfter = syncLagWarnFloor
	}
	errHint := ""
	if hs.LastError != "" {
		errHint = "; last sync error: " + hs.LastError
	}
	if hs.LastPullAt.IsZero() {
		if hs.LastError != "" {
			return ds, "the daemon has not pulled from the tracker yet" + errHint
		}
		return ds, ""
	}
	ds.Lag = max(now.Sub(hs.LastPullAt), 0).Round(time.Second)
	if ds.Lag > warnAfter {
		return ds, fmt.Sprintf("tracker sync lag %s is past %s — the board and dispatch are working from tracker data that old%s",
			ds.Lag, warnAfter, errHint)
	}
	return ds, ""
}

// renderDoctorSync writes the sync status lines.
func renderDoctorSync(b *strings.Builder, ds *doctorSync, warning string) {
	if ds != nil {
		pull := "never"
		if !ds.LastPullAt.IsZero() {
			pull = fmt.Sprintf("%s (%s ago)", ds.LastPullAt.UTC().Format(time.RFC3339), ds.Lag)
		}
		fmt.Fprintf(b, "sync: %s · last pull %s · warns after %s · pending writes %s",
			or(ds.Mode, "unknown"), pull, or(ds.WarnAfter.String(), "?"), or(ds.Pending, "0"))
		if ds.NextPush != "" {
			fmt.Fprintf(b, " · next push %s", ds.NextPush)
		}
		b.WriteString("\n")
		if ds.LastError != "" && warning == "" {
			fmt.Fprintf(b, "sync: last error: %s\n", ds.LastError)
		}
	}
	if warning != "" {
		fmt.Fprintf(b, "WARNING: %s\n", warning)
	}
}
