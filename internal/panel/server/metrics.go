package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"
)

var httpRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "maburvm_http_request_duration_seconds",
	Help:    "Panel HTTP request latency by method, route template and status.",
	Buckets: prometheus.DefBuckets,
}, []string{"method", "route", "status"})

// metricsMiddleware records request latency. The route label is the echo route
// template (e.g. /api/v1/vms/:id), never the raw path, so cardinality stays bounded.
func metricsMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		err := next(c)
		status := c.Response().Status
		var he *echo.HTTPError
		if errors.As(err, &he) {
			status = he.Code
		}
		route := c.Path()
		if route == "" {
			route = "unmatched"
		}
		httpRequestDuration.WithLabelValues(c.Request().Method, route, strconv.Itoa(status)).
			Observe(time.Since(start).Seconds())
		return err
	}
}

// metricsHandler serves the Prometheus scrape endpoint behind a bearer token.
// With no token configured the endpoint is off: the panel is internet-facing
// and metrics leak route and runtime details.
func metricsHandler(token string, db *gorm.DB) echo.HandlerFunc {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), httpRequestDuration)
	if db != nil {
		reg.MustRegister(&fleetCollector{db: db})
	}
	h := echo.WrapHandler(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return func(c echo.Context) error {
		if token == "" {
			return echo.ErrNotFound
		}
		got := c.Request().Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+token)) != 1 {
			return c.NoContent(http.StatusUnauthorized)
		}
		return h(c)
	}
}

var (
	fleetUp        = prometheus.NewDesc("maburvm_fleet_collect_success", "1 if the fleet gauges were read from the database on this scrape.", nil, nil)
	nodeStatusDesc = prometheus.NewDesc("maburvm_node_status", "Current node status (1 for the node's status label).", []string{"node", "status"}, nil)
	vmsDesc        = prometheus.NewDesc("maburvm_vms", "VMs by node and status.", []string{"node", "status"}, nil)
	nodeLastReport = prometheus.NewDesc("maburvm_node_last_report_timestamp_seconds", "Unix time of the node's latest agent metrics sample.", []string{"node"}, nil)
	// In the column order of the latest-sample query below.
	nodeSampleGauges = []*prometheus.Desc{
		prometheus.NewDesc("maburvm_node_cpu_usage_percent", "Node CPU usage from the latest agent sample.", []string{"node"}, nil),
		prometheus.NewDesc("maburvm_node_memory_usage_percent", "Node memory usage from the latest agent sample.", []string{"node"}, nil),
		prometheus.NewDesc("maburvm_node_disk_usage_percent", "Node disk usage from the latest agent sample.", []string{"node"}, nil),
		prometheus.NewDesc("maburvm_node_network_receive_bytes_per_second", "Node network receive rate from the latest agent sample.", []string{"node"}, nil),
		prometheus.NewDesc("maburvm_node_network_transmit_bytes_per_second", "Node network transmit rate from the latest agent sample.", []string{"node"}, nil),
		prometheus.NewDesc("maburvm_node_conntrack_entries", "Node conntrack table entries.", []string{"node"}, nil),
		prometheus.NewDesc("maburvm_node_conntrack_max", "Node conntrack table size.", []string{"node"}, nil),
	}
)

// fleetCollector reads node and VM state from the database at scrape time, so
// Prometheus sees the same numbers as the panel UI without a second poller.
type fleetCollector struct{ db *gorm.DB }

// Describe sends nothing, making this an unchecked collector: registering it
// must not query the database.
func (f *fleetCollector) Describe(chan<- *prometheus.Desc) {}

func (f *fleetCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.collect(ctx, ch); err != nil {
		slog.Default().Warn("metrics: fleet collect failed", "error", err)
		ch <- prometheus.MustNewConstMetric(fleetUp, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(fleetUp, prometheus.GaugeValue, 1)
}

func (f *fleetCollector) collect(ctx context.Context, ch chan<- prometheus.Metric) error {
	db := f.db.WithContext(ctx)

	var nodes []struct{ Name, Status string }
	if err := db.Raw(`SELECT name, status FROM nodes WHERE deleted_at IS NULL`).Scan(&nodes).Error; err != nil {
		return err
	}
	for _, n := range nodes {
		ch <- prometheus.MustNewConstMetric(nodeStatusDesc, prometheus.GaugeValue, 1, n.Name, n.Status)
	}

	var vms []struct {
		Node, Status string
		Count        float64
	}
	if err := db.Raw(`SELECT n.name AS node, v.status, COUNT(*) AS count
		FROM vms v JOIN nodes n ON n.id = v.node_id
		WHERE v.deleted_at IS NULL GROUP BY n.name, v.status`).Scan(&vms).Error; err != nil {
		return err
	}
	for _, v := range vms {
		ch <- prometheus.MustNewConstMetric(vmsDesc, prometheus.GaugeValue, v.Count, v.Node, v.Status)
	}

	// Latest sample per node. The recency bound keeps this a short index scan
	// (265ms unbounded on prod vs 8ms); a node silent for an hour drops out.
	rows, err := db.Raw(`SELECT n.name, m.cpu_usage, m.memory_usage, m.disk_usage,
			m.network_rx_bytes_per_sec, m.network_tx_bytes_per_sec, m.conntrack_count, m.conntrack_max, m.recorded_at
		FROM node_metrics m JOIN nodes n ON n.id = m.node_id
		WHERE n.deleted_at IS NULL AND m.recorded_at > ?
		  AND m.recorded_at = (SELECT MAX(recorded_at) FROM node_metrics WHERE node_id = m.node_id)`,
		time.Now().Add(-time.Hour)).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var vals [7]float64
		var at time.Time
		if err := rows.Scan(&name, &vals[0], &vals[1], &vals[2], &vals[3], &vals[4], &vals[5], &vals[6], &at); err != nil {
			return err
		}
		for i, g := range nodeSampleGauges {
			ch <- prometheus.MustNewConstMetric(g, prometheus.GaugeValue, vals[i], name)
		}
		ch <- prometheus.MustNewConstMetric(nodeLastReport, prometheus.GaugeValue, float64(at.Unix()), name)
	}
	return rows.Err()
}
