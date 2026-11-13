package service

import (
	"strings"
	"testing"
)

func TestBrandingNormalize(t *testing.T) {
	png := "data:image/png;base64,iVBORw0KGgo="
	ok := []Branding{
		{},
		{Name: "  Acme Cloud ", Accent: "#0A7CFF", Logo: png, SupportURL: "https://help.example.com", SupportEmail: "help@example.com"},
	}
	for i, b := range ok {
		if err := b.Normalize(); err != nil {
			t.Errorf("ok[%d]: %v", i, err)
		}
	}
	if b := ok[1]; b.Normalize() != nil || b.Name != "Acme Cloud" || b.Accent != "#0a7cff" {
		t.Errorf("not normalised: %+v", b)
	}

	bad := map[string]Branding{
		"named colour":    {Accent: "red"},
		"script in logo":  {Logo: "data:text/html;base64,PHNjcmlwdD4="},
		"remote logo":     {Logo: "https://example.com/logo.png"},
		"huge logo":       {Logo: "data:image/png;base64," + strings.Repeat("A", maxBrandImage)},
		"javascript link": {SupportURL: "javascript:alert(1)"},
		"bad email":       {SupportEmail: "not an address"},
		"long name":       {Name: strings.Repeat("x", 61)},
	}
	for name, b := range bad {
		if err := b.Normalize(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
