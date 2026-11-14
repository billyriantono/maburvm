package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBrandingSignInPicture(t *testing.T) {
	png := "data:image/png;base64,iVBORw0KGgo="

	// Only the three known styles are accepted.
	b := Branding{Name: "Acme", LoginArt: "confetti"}
	require.Error(t, b.Normalize())
	for _, style := range []string{"", "traces", "logo", "image"} {
		b = Branding{Name: "Acme", LoginArt: style, LoginImage: png}
		require.NoError(t, b.Normalize(), style)
	}

	// The picture must be a photo format and within the size limit; "none" is
	// the request to remove it and is not judged as a picture.
	b = Branding{Name: "Acme", LoginImage: "data:image/svg+xml;base64,PHN2Zy8+"}
	require.Error(t, b.Normalize())
	b = Branding{Name: "Acme", LoginImage: "data:image/png;base64," + strings.Repeat("A", maxLoginImage)}
	require.Error(t, b.Normalize())
	b = Branding{Name: "Acme", LoginImage: RemoveLoginImage}
	require.NoError(t, b.Normalize())

	// What the public sees: no picture inline, an address that follows it.
	one := Branding{Name: "Acme", LoginImage: png}.Public()
	require.Empty(t, one.LoginImage)
	require.True(t, strings.HasPrefix(one.LoginImageURL, "/api/v1/branding/login-image?v="))
	two := Branding{Name: "Acme", LoginImage: png + "AAAA"}.Public()
	require.NotEqual(t, one.LoginImageURL, two.LoginImageURL)
	require.Empty(t, Branding{Name: "Acme"}.Public().LoginImageURL)
}
