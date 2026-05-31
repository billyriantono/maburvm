package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/service"
)

// EventsHandler streams real-time platform events to the browser via SSE.
type EventsHandler struct {
	vmService *service.VMService
}

// NewEventsHandler creates a new EventsHandler.
func NewEventsHandler(vmService *service.VMService) *EventsHandler {
	return &EventsHandler{vmService: vmService}
}

const (
	// vmStatusPollInterval is how often the stream re-checks VM status.
	vmStatusPollInterval = 3 * time.Second
	// vmStatusKeepAlive is how often a comment is sent to keep idle connections open.
	vmStatusKeepAlive = 25 * time.Second
	// vmStatusSnapshotLimit bounds how many VMs a single snapshot reports.
	vmStatusSnapshotLimit = 1000
)

// StreamVMStatus streams VM status changes as Server-Sent Events. It emits a
// snapshot on connect and again whenever any VM's status changes, plus periodic
// keepalive comments. The connection ends when the client disconnects.
// Route: GET /api/v1/events/vm-status
func (h *EventsHandler) StreamVMStatus(c echo.Context) error {
	res := c.Response()
	res.Header().Set("Content-Type", "text/event-stream")
	res.Header().Set("Cache-Control", "no-cache")
	res.Header().Set("Connection", "keep-alive")
	res.Header().Set("X-Accel-Buffering", "no") // don't let nginx buffer the stream
	res.WriteHeader(http.StatusOK)
	res.Flush()

	ctx := c.Request().Context()

	last := h.statusSnapshot(ctx)
	if err := writeSSEData(res, "vm_status", last); err != nil {
		return nil
	}

	poll := time.NewTicker(vmStatusPollInterval)
	defer poll.Stop()
	keepAlive := time.NewTicker(vmStatusKeepAlive)
	defer keepAlive.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			current := h.statusSnapshot(ctx)
			if !sameStatusMap(last, current) {
				if err := writeSSEData(res, "vm_status", current); err != nil {
					return nil
				}
				last = current
			}
		case <-keepAlive.C:
			if _, err := res.Write([]byte(": keepalive\n\n")); err != nil {
				return nil
			}
			res.Flush()
		}
	}
}

// statusSnapshot returns a map of VM ID -> status for all VMs. On error it
// returns an empty map so the stream keeps running (transient DB hiccups don't
// kill the connection).
func (h *EventsHandler) statusSnapshot(ctx context.Context) map[string]string {
	resp, err := h.vmService.ListVMs(ctx, &service.ListVMsRequest{Limit: vmStatusSnapshotLimit})
	if err != nil {
		return map[string]string{}
	}
	statuses := make(map[string]string, len(resp.VMs))
	for i := range resp.VMs {
		statuses[resp.VMs[i].ID] = string(resp.VMs[i].Status)
	}
	return statuses
}

// sameStatusMap reports whether two status snapshots are identical.
func sameStatusMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for id, status := range a {
		if b[id] != status {
			return false
		}
	}
	return true
}

// writeSSEData writes one SSE "data:" frame carrying a JSON {type,data} object.
func writeSSEData(res *echo.Response, eventType string, data interface{}) error {
	payload, err := json.Marshal(map[string]interface{}{
		"type":      eventType,
		"data":      data,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(res, "data: %s\n\n", payload); err != nil {
		return err
	}
	res.Flush()
	return nil
}
