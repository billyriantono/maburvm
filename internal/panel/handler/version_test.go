package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/service"
)

func TestConfirmUpdate(t *testing.T) {
	h := &VersionHandler{confirmWait: 200 * time.Millisecond, confirmPoll: 10 * time.Millisecond}
	const want = "0123456789abcdef0123456789abcdef01234567"

	// Down while it restarts, then up on the new build.
	tries := 0
	ok := h.confirmUpdate("n1", "node-a", want, "fedcba9", func(context.Context) (string, error) {
		tries++
		if tries < 3 {
			return "", errors.New("connection refused")
		}
		return want, nil
	})
	if !ok {
		t.Fatal("a node that comes back on the new build must be confirmed")
	}

	// Rolled back: back on the old build when the wait is over. Alerts is
	// unset here, which must not panic.
	if h.confirmUpdate("n1", "node-a", want, "fedcba9", func(context.Context) (string, error) { return "fedcba9876543210", nil }) {
		t.Fatal("a node still on its old build must not be confirmed")
	}
	// An unstamped panel build can never be confirmed.
	if h.confirmUpdate("n1", "node-a", "", "", func(context.Context) (string, error) { return "", nil }) {
		t.Fatal("an empty commit must not count as a match")
	}
}

// A test binary carries no revision, like a panel image built without one: it
// must refuse before contacting the node.
func TestUpdateAgentRefusesUnstampedPanel(t *testing.T) {
	h := &VersionHandler{nodeService: &service.NodeService{}}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec)
	c.SetParamNames("id")
	c.SetParamValues("n1")
	if err := h.UpdateAgent(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d %s, want 409", rec.Code, rec.Body.String())
	}
}
