package service

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"

	"gorm.io/gorm"
)

// BrandingSection is the system_settings row that holds the white-label
// identity. It is public by design: the sign-in page reads it before anyone
// has logged in, so nothing secret may ever be added to it.
const BrandingSection = "branding"

// DefaultBrandName is used wherever no name has been set.
const DefaultBrandName = "MaburVM"

// maxBrandImage caps one embedded image (as a data URL). Logos live in the
// settings row so a fresh install needs no object storage to be branded.
// ponytail: stored inline in Postgres; move to object storage if someone
// needs logos larger than this.
const maxBrandImage = 400 * 1024

// Branding is the operator's identity for the panel.
type Branding struct {
	Name          string `json:"name"`
	Tagline       string `json:"tagline"`
	Logo          string `json:"logo"`    // data URL; empty = generated monogram
	Favicon       string `json:"favicon"` // data URL; empty = generated from the monogram
	Accent        string `json:"accent"`  // #rrggbb; empty = built-in neutral
	LoginHeadline string `json:"login_headline"`
	LoginMessage  string `json:"login_message"`
	SupportURL    string `json:"support_url"`
	SupportEmail  string `json:"support_email"`
	FooterText    string `json:"footer_text"`
	// What fills the picture side of the sign-in page: "" or "traces" for the
	// built-in artwork in the operator's colour, "logo" for their logo on their
	// colour, "image" for a picture of their own.
	LoginArt string `json:"login_art"`
	// The operator's own picture, as a data URL. On save: empty keeps the stored
	// one, "none" removes it. Never sent with the public branding (it would ride
	// along in every page); LoginImageURL points at it instead.
	LoginImage    string `json:"login_image"`
	LoginImageURL string `json:"login_image_url,omitempty"`
}

// maxLoginImage caps the sign-in picture (as a data URL). It is a full-height
// photograph, so it gets more room than a logo.
const maxLoginImage = 1200 * 1024

// RemoveLoginImage is what a save sends to drop the stored sign-in picture.
const RemoveLoginImage = "none"

// Public returns the branding as anyone may see it: the same, with the
// sign-in picture replaced by an address that changes when the picture does.
func (b Branding) Public() Branding {
	if b.LoginImage != "" {
		sum := sha1.Sum([]byte(b.LoginImage))
		b.LoginImageURL = "/api/v1/branding/login-image?v=" + hex.EncodeToString(sum[:6])
	}
	b.LoginImage = ""
	return b
}

var (
	hexColor     = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	brandDataURL = regexp.MustCompile(`^data:image/(png|jpeg|webp|svg\+xml|x-icon|vnd\.microsoft\.icon);base64,[A-Za-z0-9+/=]+$`)
	photoDataURL = regexp.MustCompile(`^data:image/(png|jpeg|webp);base64,[A-Za-z0-9+/=]+$`)
)

// Normalize trims the fields and reports the first thing wrong with them, in
// words an operator can act on.
func (b *Branding) Normalize() error {
	for _, f := range []*string{&b.Name, &b.Tagline, &b.Accent, &b.LoginHeadline, &b.LoginMessage, &b.SupportURL, &b.SupportEmail, &b.FooterText} {
		*f = strings.TrimSpace(*f)
	}
	limits := []struct {
		label string
		value string
		max   int
	}{
		{"Name", b.Name, 60}, {"Tagline", b.Tagline, 80}, {"Sign-in headline", b.LoginHeadline, 80},
		{"Sign-in message", b.LoginMessage, 240}, {"Footer text", b.FooterText, 160},
	}
	for _, l := range limits {
		if len([]rune(l.value)) > l.max {
			return fmt.Errorf("%s is longer than %d characters", l.label, l.max)
		}
	}
	if b.Accent != "" && !hexColor.MatchString(b.Accent) {
		return fmt.Errorf("Colour must look like #1a2b3c")
	}
	b.Accent = strings.ToLower(b.Accent)
	for label, img := range map[string]string{"Logo": b.Logo, "Favicon": b.Favicon} {
		if img == "" {
			continue
		}
		if len(img) > maxBrandImage {
			return fmt.Errorf("%s is too large (limit is about 300 KB)", label)
		}
		if !brandDataURL.MatchString(img) {
			return fmt.Errorf("%s must be a PNG, JPEG, WebP, SVG or ICO image", label)
		}
	}
	switch b.LoginArt {
	case "", "traces", "logo", "image":
	default:
		return fmt.Errorf("Sign-in picture style must be traces, logo or image")
	}
	if b.LoginImage != "" && b.LoginImage != RemoveLoginImage {
		if len(b.LoginImage) > maxLoginImage {
			return fmt.Errorf("Sign-in picture is too large (limit is about 850 KB)")
		}
		if !photoDataURL.MatchString(b.LoginImage) {
			return fmt.Errorf("Sign-in picture must be a PNG, JPEG or WebP image")
		}
	}
	if b.SupportURL != "" {
		u, err := url.Parse(b.SupportURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("Support link must start with http:// or https://")
		}
	}
	if b.SupportEmail != "" {
		if a, err := mail.ParseAddress(b.SupportEmail); err != nil || a.Address != b.SupportEmail {
			return fmt.Errorf("Support email is not a valid address")
		}
	}
	return nil
}

// LoadBranding returns the stored branding with the default name filled in.
// A missing or unreadable row is not an error: the panel is simply unbranded.
func LoadBranding(db *gorm.DB) Branding {
	var raw string
	var b Branding
	if db != nil {
		if err := db.Raw("SELECT data::text FROM system_settings WHERE section = ?", BrandingSection).Scan(&raw).Error; err == nil && raw != "" {
			_ = json.Unmarshal([]byte(raw), &b)
		}
	}
	if b.Name == "" {
		b.Name = DefaultBrandName
	}
	return b
}

// BrandingRefusal is a save turned down for a reason the operator can fix,
// as opposed to the database failing.
type BrandingRefusal struct{ Reason string }

func (e *BrandingRefusal) Error() string { return e.Reason }

// SaveBranding validates and stores the branding.
func SaveBranding(db *gorm.DB, b *Branding) error {
	if err := b.Normalize(); err != nil {
		return err
	}
	switch b.LoginImage {
	case "":
		b.LoginImage = LoadBranding(db).LoginImage
	case RemoveLoginImage:
		b.LoginImage = ""
	}
	if b.LoginArt == "image" && b.LoginImage == "" {
		return &BrandingRefusal{"Choose a picture for the sign-in page, or pick another style"}
	}
	b.LoginImageURL = ""
	data, _ := json.Marshal(b)
	return db.Exec(
		"INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?::jsonb, NOW()) "+
			"ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()",
		BrandingSection, string(data),
	).Error
}
