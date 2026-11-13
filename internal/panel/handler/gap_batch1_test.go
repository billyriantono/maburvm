package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
)

func ctxWithRole(role models.UserRole) echo.Context {
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder())
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.New(), Role: role})
	return c
}

func TestCheckISOSource(t *testing.T) {
	client, admin := ctxWithRole(models.RoleClient), ctxWithRole(models.RoleAdmin)
	for ref, wantOK := range map[string]bool{
		"https://example.com/debian.iso":  true,
		"http://203.0.113.9/x.iso":        true,
		"/var/lib/libvirt/images/a.qcow2": false,
		"vm://1234":                       false,
		"s3://bucket/key":                 false,
		"file:///etc/shadow":              false,
		"http://127.0.0.1/x.iso":          false,
		"http://10.0.0.5/x.iso":           false,
		"http://169.254.169.254/latest":   false,
		"http://localhost/x.iso":          false,
		"http://[::1]/x.iso":              false,
	} {
		if err := checkISOSource(client, ref); (err == nil) != wantOK {
			t.Errorf("client %q: ok=%v, want %v", ref, err == nil, wantOK)
		}
	}
	if err := checkISOSource(admin, "/var/lib/libvirt/templates/rescue.iso"); err != nil {
		t.Errorf("admin on-node path refused: %v", err)
	}
}

func TestReservedHostPort(t *testing.T) {
	for p, want := range map[int]bool{22: true, 80: true, 1023: true, 50051: true, 5901: true, 16509: true, 49152: true, 1024: false, 8080: false, 25565: false} {
		if got := reservedHostPort(p); got != want {
			t.Errorf("port %d: reserved=%v, want %v", p, got, want)
		}
	}
}
