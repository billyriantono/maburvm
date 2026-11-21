package service

import (
	"context"
	"time"
)

// RawMetricsWindow is how far back a graph is drawn from the raw samples;
// longer windows read the hourly averages, which are kept for the portal's
// graph history (PortalSettings.GraphDays).
const RawMetricsWindow = 7 * 24 * time.Hour

// rollupHours averages each hour that has ended into vm_metrics_hourly, once
// the hour is over, and drops averages older than the graph history. The
// first run after a start also averages what the raw samples still hold, so
// history that predates this feature is not lost when they are pruned.
func (c *MetricsCollector) rollupHours(ctx context.Context) {
	hour := time.Now().UTC().Truncate(time.Hour)
	if !c.rolledHour.Before(hour) {
		return
	}
	from := c.rolledHour.Add(-time.Hour) // the hour before too, for samples stored late
	if c.rolledHour.IsZero() {
		from = hour.Add(-RawMetricsWindow)
		if c.retention > 0 && c.retention < RawMetricsWindow {
			from = hour.Add(-c.retention)
		}
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := c.repo.RollupVMHours(rctx, from, hour); err != nil {
		c.logger.Warn("metrics collector: hourly averages failed", "error", err)
		return
	}
	c.rolledHour = hour
	days := LoadPortalSettings(rctx, c.db).GraphDays()
	if _, err := c.repo.PruneVMHoursBefore(rctx, hour.Add(-time.Duration(days)*24*time.Hour)); err != nil {
		c.logger.Warn("metrics collector: pruning hourly averages failed", "error", err)
	}
}
