package service

import (
	"context"
	"time"
)

// RawMetricsWindow is how far back a graph is drawn from the raw samples;
// longer windows read the hourly averages, which are kept for the portal's
// graph history (PortalSettings.GraphDays).
const RawMetricsWindow = 7 * 24 * time.Hour

// DailyMetricsDays is how long daily averages are kept: two years. Windows
// longer than the hourly history read them.
const DailyMetricsDays = 730

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
	c.rollupDays(rctx, hour, days)
	if _, err := c.repo.PruneVMHoursBefore(rctx, hour.Add(-time.Duration(days)*24*time.Hour)); err != nil {
		c.logger.Warn("metrics collector: pruning hourly averages failed", "error", err)
	}
}

// rollupDays averages each UTC day that has ended into vm_metrics_daily from
// the hourly averages, before those are pruned, and drops daily averages
// older than two years. The first run after a start averages every day the
// hourly averages still hold.
func (c *MetricsCollector) rollupDays(ctx context.Context, hour time.Time, hourlyDays int) {
	day := hour.Truncate(24 * time.Hour)
	if !c.rolledDay.Before(day) {
		return
	}
	from := c.rolledDay.Add(-24 * time.Hour) // the day before too, for hours rolled up late
	if c.rolledDay.IsZero() {
		from = day.Add(-time.Duration(hourlyDays) * 24 * time.Hour)
	}
	if err := c.repo.RollupVMDays(ctx, from, day); err != nil {
		c.logger.Warn("metrics collector: daily averages failed", "error", err)
		return
	}
	c.rolledDay = day
	if _, err := c.repo.PruneVMDaysBefore(ctx, day.Add(-DailyMetricsDays*24*time.Hour)); err != nil {
		c.logger.Warn("metrics collector: pruning daily averages failed", "error", err)
	}
}
