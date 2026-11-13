package handler

import (
	"testing"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
)

func TestCSVCellDefusesFormulas(t *testing.T) {
	for in, want := range map[string]string{
		"web-01":            "web-01",
		"=HYPERLINK(\"x\")": "'=HYPERLINK(\"x\")",
		"+1":                "'+1",
		"-cmd":              "'-cmd",
		"@sum":              "'@sum",
		"":                  "",
	} {
		if got := csvCell(in); got != want {
			t.Errorf("csvCell(%q) = %q, want %q", in, got, want)
		}
	}
	if csvCell(nil) != "" || csvCell([]byte("=x")) != "'=x" {
		t.Error("nil or bytes handled wrongly")
	}
}

func TestUserExpiry(t *testing.T) {
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	if (&models.User{}).Expired() || (&models.User{ExpiresAt: &future}).Expired() || !(&models.User{ExpiresAt: &past}).Expired() {
		t.Fatal("expiry is wrong")
	}
}

func TestWorkerOverridesStayInRange(t *testing.T) {
	c := queue.DefaultConfig("")
	c.ApplyWorkerOverrides(map[string]int{queue.QueueCritical: 5, queue.QueueBatch: 0, queue.QueueAudit: 9999})
	if c.CriticalWorkers != 5 || c.BatchWorkers != 10 || c.AuditWorkers != 20 {
		t.Fatalf("got %+v", c)
	}
}
