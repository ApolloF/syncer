package backup

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Pruning goes by the PC's clock: one that jumped a year ahead would take
// every restore point for expired, and deleting them in the backup deletes
// them for every PC. So before pruning, the time is checked with Google (the
// Date of an HTTPS response, which a wrong clock can't be fooled about either:
// its TLS check fails), and nothing is pruned when the clocks differ or the
// check can't be made. Only pruning waits; the backup itself runs as always.

// clockSlack is how far off the clock may be. Expiry and thinning go by days,
// so a few hours don't matter.
const clockSlack = 6 * time.Hour

// clockURL answers with an empty response; only its Date is used. Nothing
// about the PC or its saves is sent.
const clockURL = "https://www.google.com/generate_204"

// netTime is a variable so tests can do without the network.
var netTime = func(ctx context.Context) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, clockURL, nil)
	if err != nil {
		return time.Time{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	resp.Body.Close()
	return http.ParseTime(resp.Header.Get("Date"))
}

// clockErr says why the clock can't be trusted to prune by at now, if it can't.
func clockErr(ctx context.Context, now time.Time) error {
	t, err := netTime(ctx)
	if err != nil {
		return fmt.Errorf("couldn't check the PC's clock: %w", err)
	}
	if d := now.Sub(t); d > clockSlack || d < -clockSlack {
		way := "ahead"
		if d < 0 {
			way, d = "behind", -d
		}
		return fmt.Errorf("the PC's clock is %s %s", offBy(d), way)
	}
	return nil
}

// offBy says d in days, or hours when shorter than two days.
func offBy(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("%d days", int(d/(24*time.Hour)))
	}
	return fmt.Sprintf("%d hours", int(d/time.Hour))
}
