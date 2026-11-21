package handler

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// writeCSV answers rows as a CSV download named name-<today>.csv.
func writeCSV(c echo.Context, name string, header []string, rows [][]any) error {
	h := c.Response().Header()
	h.Set(echo.HeaderContentType, "text/csv; charset=utf-8")
	h.Set(echo.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s-%s.csv"`, name, time.Now().UTC().Format("2006-01-02")))
	c.Response().WriteHeader(http.StatusOK)
	w := csv.NewWriter(c.Response())
	_ = w.Write(header)
	record := make([]string, len(header))
	for _, r := range rows {
		for i, v := range r {
			record[i] = csvCell(v)
		}
		_ = w.Write(record)
	}
	w.Flush()
	return w.Error()
}

// RegisterReportRoutes mounts the capacity and customer usage reports.
func RegisterReportRoutes(e *echo.Echo, db *gorm.DB) {
	g := e.Group("/api/v1/reports", middleware.RequireAuth(db), middleware.RequirePermission(middleware.PermStaff),
		middleware.RequirePermission("report:read"))

	g.GET("/capacity", func(c echo.Context) error {
		scope := c.QueryParam("scope")
		if scope == "" {
			scope = service.CapacityByNode
		}
		days, _ := strconv.Atoi(c.QueryParam("days"))
		if days <= 0 {
			days = 90
		}
		days = min(days, 730)
		series, err := service.CapacityReport(c.Request().Context(), db, scope, days)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
		}
		if c.QueryParam("format") == "csv" {
			var rows [][]any
			for _, s := range series {
				for _, p := range s.Points {
					rows = append(rows, []any{p.Day, s.Name, p.VMs, p.VCPUAllocated, p.VCPUTotal, p.RAMAllocatedMB, p.RAMTotalMB,
						p.DiskAllocatedGB, p.DiskTotalGB, p.DiskUsedGB, p.CPUUsage, p.MemoryUsage})
				}
			}
			return writeCSV(c, "capacity-"+scope, []string{"day", scope, "vms", "vcpu_allocated", "vcpu_total", "ram_allocated_mb", "ram_total_mb",
				"disk_allocated_gb", "disk_total_gb", "disk_used_gb", "cpu_usage_percent", "memory_usage_percent"}, rows)
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{
			"scope": scope, "days": days, "series": series,
		}})
	})

	g.GET("/usage", func(c echo.Context) error {
		month := time.Now().UTC()
		if raw := c.QueryParam("month"); raw != "" {
			t, err := time.Parse("2006-01", raw)
			if err != nil {
				return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "month must be YYYY-MM"})
			}
			month = t
		}
		rows, err := service.CustomerUsageReport(c.Request().Context(), db, month)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "The usage report could not be read"})
		}
		if c.QueryParam("format") == "csv" {
			out := make([][]any, len(rows))
			for i, r := range rows {
				out[i] = []any{r.Email, r.Name, r.VMs, r.VCPU, r.RAMMB, r.DiskGB, r.TransferBytes}
			}
			return writeCSV(c, "usage-"+month.Format("2006-01"), []string{"email", "name", "vms", "vcpu", "ram_mb", "disk_gb", "transfer_bytes"}, out)
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{
			"month": month.Format("2006-01"), "customers": rows,
		}})
	})
}
