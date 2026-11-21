package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
)

// Announcements: notices staff write for customers, shown in the customer
// area to everyone or to the owners of VMs on chosen nodes, regions, plans or
// VMs, and optionally emailed through the email queue.

// Kinds of announcement. A marketing one is never emailed to an account that
// unsubscribed; a service notice (maintenance, an incident) always is.
const (
	AnnouncementService   = "service"
	AnnouncementMarketing = "marketing"
)

// Audience types.
const (
	AudienceAll     = "all"
	AudienceNodes   = "nodes"
	AudienceRegions = "regions"
	AudiencePlans   = "plans"
	AudienceVMs     = "vms"
)

// Audience says who an announcement is for.
type Audience struct {
	Type string   `json:"type"`
	IDs  []string `json:"ids,omitempty"`
}

// Announcement is one notice.
type Announcement struct {
	ID          string         `json:"id" gorm:"primaryKey"`
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	Kind        string         `json:"kind"`
	Audience    Audience       `json:"audience" gorm:"type:jsonb;serializer:json"`
	StartsAt    time.Time      `json:"starts_at"`
	EndsAt      *time.Time     `json:"ends_at"`
	EmailedAt   *time.Time     `json:"emailed_at"`
	EmailQueued int            `json:"email_queued"`
	CreatedBy   *string        `json:"created_by"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"-"`
}

func (Announcement) TableName() string { return "announcements" }

// CustomerAnnouncement is an announcement as a customer sees it: without its
// audience, which names other customers' VMs and the nodes.
type CustomerAnnouncement struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Body     string     `json:"body"`
	Kind     string     `json:"kind"`
	StartsAt time.Time  `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
}

// ForCustomer is a as a customer sees it.
func (a Announcement) ForCustomer() CustomerAnnouncement {
	return CustomerAnnouncement{ID: a.ID, Title: a.Title, Body: a.Body, Kind: a.Kind, StartsAt: a.StartsAt, EndsAt: a.EndsAt}
}

// AnnouncementInput is what staff write.
type AnnouncementInput struct {
	Title    string     `json:"title"`
	Body     string     `json:"body"`
	Kind     string     `json:"kind"`
	Audience Audience   `json:"audience"`
	StartsAt *time.Time `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
}

// ErrAnnouncementNotFound names an announcement that does not exist.
var ErrAnnouncementNotFound = errors.New("announcement not found")

// ErrInvalidAnnouncement is an announcement that cannot be saved as written.
var ErrInvalidAnnouncement = errors.New("invalid announcement")

func (in *AnnouncementInput) normalize() error {
	in.Title = strings.TrimSpace(in.Title)
	in.Body = strings.TrimSpace(strings.ReplaceAll(in.Body, "\r\n", "\n"))
	bad := func(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidAnnouncement, msg) }
	switch {
	case in.Title == "" || len([]rune(in.Title)) > 150 || strings.ContainsAny(in.Title, "\r\n"):
		return bad("the title is one line of 1 to 150 characters")
	case in.Body == "" || len([]rune(in.Body)) > 4000:
		return bad("the message is 1 to 4000 characters")
	}
	if in.Kind == "" {
		in.Kind = AnnouncementService
	}
	if in.Kind != AnnouncementService && in.Kind != AnnouncementMarketing {
		return bad("kind is service or marketing")
	}
	if in.Audience.Type == "" {
		in.Audience.Type = AudienceAll
	}
	switch in.Audience.Type {
	case AudienceAll:
		in.Audience.IDs = nil
	case AudienceNodes, AudienceRegions, AudiencePlans, AudienceVMs:
		if len(in.Audience.IDs) == 0 || len(in.Audience.IDs) > 500 {
			return bad("choose between 1 and 500 " + in.Audience.Type)
		}
		for _, id := range in.Audience.IDs {
			if _, err := uuid.Parse(id); err != nil {
				return bad(fmt.Sprintf("%q is not an id", id))
			}
		}
	default:
		return bad("the audience is all, nodes, regions, plans or vms")
	}
	if in.StartsAt != nil && in.EndsAt != nil && !in.EndsAt.After(*in.StartsAt) {
		return bad("it must end after it starts")
	}
	return nil
}

// audienceUserIDs is a subquery of the customer accounts an audience covers.
func audienceUserIDs(db *gorm.DB, a Audience) *gorm.DB {
	clients := db.Table("users").Select("CAST(id AS TEXT) AS id").
		Where("role = 'client' AND deleted_at IS NULL AND expires_at IS NULL")
	if a.Type == AudienceAll {
		return clients
	}
	column := map[string]string{
		AudienceNodes: "CAST(v.node_id AS TEXT)", AudienceRegions: "CAST(n.region_id AS TEXT)",
		AudiencePlans: "CAST(v.plan_id AS TEXT)", AudienceVMs: "CAST(v.id AS TEXT)",
	}[a.Type]
	owners := db.Table("vms v").Select("CAST(v.user_id AS TEXT) AS user_id").
		Joins("LEFT JOIN nodes n ON CAST(n.id AS TEXT) = CAST(v.node_id AS TEXT)").
		Where("v.deleted_at IS NULL AND "+column+" IN ?", a.IDs)
	// The people working in those accounts run the same VMs, so they are told
	// too. ponytail: a member limited to some VMs also hears about the
	// account's others; match vm_ids/project_ids if that ever matters.
	members := db.Table("account_members").Select("CAST(user_id AS TEXT)").
		Where("accepted_at IS NOT NULL AND user_id IS NOT NULL AND CAST(account_id AS TEXT) IN (?)", owners)
	return clients.Where("(CAST(id AS TEXT) IN (?) OR CAST(id AS TEXT) IN (?))", owners, members)
}

// ListAnnouncements returns every announcement, newest first.
func ListAnnouncements(ctx context.Context, db *gorm.DB) ([]Announcement, error) {
	out := []Announcement{}
	err := db.WithContext(ctx).Order("starts_at DESC, created_at DESC").Limit(200).Find(&out).Error
	return out, err
}

// SaveAnnouncement creates (id "") or rewrites an announcement.
func SaveAnnouncement(ctx context.Context, db *gorm.DB, id string, in AnnouncementInput, by string) (*Announcement, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	a := &Announcement{}
	if id != "" {
		if _, err := uuid.Parse(id); err != nil {
			return nil, ErrAnnouncementNotFound
		}
		if err := db.WithContext(ctx).First(a, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrAnnouncementNotFound
			}
			return nil, err
		}
	} else {
		a.ID, a.CreatedAt, a.CreatedBy, a.StartsAt = uuid.New().String(), now, strPtrOrNil(by), now
	}
	a.Title, a.Body, a.Kind, a.Audience, a.EndsAt, a.UpdatedAt = in.Title, in.Body, in.Kind, in.Audience, in.EndsAt, now
	if in.StartsAt != nil {
		a.StartsAt = in.StartsAt.UTC()
	}
	if err := db.WithContext(ctx).Save(a).Error; err != nil {
		return nil, fmt.Errorf("could not save the announcement: %w", err)
	}
	return a, nil
}

// DeleteAnnouncement takes an announcement down.
func DeleteAnnouncement(ctx context.Context, db *gorm.DB, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrAnnouncementNotFound
	}
	res := db.WithContext(ctx).Where("id = ?", id).Delete(&Announcement{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrAnnouncementNotFound
	}
	return nil
}

// CurrentAnnouncements are those showing now that a customer account is in
// the audience of.
func CurrentAnnouncements(ctx context.Context, db *gorm.DB, userID string) ([]Announcement, error) {
	now := time.Now().UTC()
	var live []Announcement
	if err := db.WithContext(ctx).Where("starts_at <= ? AND (ends_at IS NULL OR ends_at > ?)", now, now).
		Order("starts_at DESC").Limit(50).Find(&live).Error; err != nil {
		return nil, err
	}
	out := []Announcement{}
	for _, a := range live {
		var n int64
		if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM (?) AS aud WHERE aud.id = ?",
			audienceUserIDs(db.WithContext(ctx), a.Audience), userID).Scan(&n).Error; err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, a)
		}
	}
	return out, nil
}

// recipient is an account an announcement is emailed to.
type recipient struct {
	ID, Email, Name string
}

// recipients are the accounts an announcement of kind would be emailed to,
// those who unsubscribed from marketing left out of a marketing one.
func recipients(ctx context.Context, db *gorm.DB, a Audience, kind string) ([]recipient, error) {
	q := db.WithContext(ctx).Table("users").Select("CAST(id AS TEXT) AS id, email, COALESCE(name, '') AS name").
		Where("CAST(id AS TEXT) IN (?) AND email <> ''", audienceUserIDs(db.WithContext(ctx), a))
	if kind == AnnouncementMarketing {
		q = q.Where("marketing_opt_out = false")
	}
	var out []recipient
	err := q.Order("email").Scan(&out).Error
	return out, err
}

// AnnouncementPreview is who an announcement reaches and its email.
type AnnouncementPreview struct {
	Recipients int           `json:"recipients"`
	Sample     []string      `json:"sample"`
	Email      RenderedEmail `json:"email"`
}

// PreviewAnnouncement counts who an announcement reaches by email and renders
// the message the first of them would get.
func PreviewAnnouncement(ctx context.Context, db *gorm.DB, in AnnouncementInput) (*AnnouncementPreview, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	rs, err := recipients(ctx, db, in.Audience, in.Kind)
	if err != nil {
		return nil, err
	}
	out := &AnnouncementPreview{Recipients: len(rs), Sample: []string{}}
	for i := 0; i < len(rs) && i < 10; i++ {
		out.Sample = append(out.Sample, rs[i].Email)
	}
	sample := recipient{ID: uuid.Nil.String(), Email: "customer@example.com", Name: "Sam"}
	if len(rs) > 0 {
		sample = rs[0]
	}
	out.Email = renderAnnouncement(db.WithContext(ctx), in.Title, in.Body, in.Kind, sample)
	return out, nil
}

func renderAnnouncement(db *gorm.DB, title, body, kind string, r recipient) RenderedEmail {
	brand := LoadBranding(db)
	publicURL := PublicURL()
	link := ""
	if publicURL != "" {
		link = publicURL + "/login"
	}
	if kind == AnnouncementMarketing && publicURL != "" {
		body += "\n\nYou receive news like this as a customer of {{brand}}. To stop it, open " +
			publicURL + "/unsubscribe?token=" + UnsubscribeToken(r.ID) + " . Notices about your service still reach you."
	}
	return RenderEmail(EmailTemplate{Subject: title, Body: body, Button: "Open your account"}, brand,
		EmailData{Name: r.Name, Email: r.Email, Link: link, LogoURL: EmailLogoURL(brand, publicURL)})
}

// AnnouncementTemplate is the email log template of an announcement's emails.
func AnnouncementTemplate(id string) string { return "announcement:" + id }

// ErrNoUnsubscribeLink refuses a marketing email that could not carry the
// way to stop it.
var ErrNoUnsubscribeLink = errors.New("a marketing email needs the panel's public address for its unsubscribe link: set it under System settings, Panel")

// ErrAnnouncementSending refuses sending an announcement whose emails are
// still being queued: both runs would see the same people as not yet emailed.
var ErrAnnouncementSending = errors.New("this announcement's emails are still being queued; try again in a minute")

// announcementSending holds the announcements being fanned out now.
var announcementSending sync.Map

// EmailAnnouncement queues the announcement's email to everyone in its
// audience who has not had it yet, spread at perMinute, and returns how many
// it queues. The queueing runs in the background; sending again is safe.
// ponytail: the fan-out lives in this process; a restart part-way leaves the
// rest unqueued until someone sends again, which skips who already had it.
func EmailAnnouncement(ctx context.Context, db *gorm.DB, id string, perMinute int) (int, error) {
	if _, err := uuid.Parse(id); err != nil {
		return 0, ErrAnnouncementNotFound
	}
	if _, busy := announcementSending.LoadOrStore(id, true); busy {
		return 0, ErrAnnouncementSending
	}
	handedOff := false
	defer func() {
		if !handedOff {
			announcementSending.Delete(id)
		}
	}()
	var a Announcement
	if err := db.WithContext(ctx).First(&a, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrAnnouncementNotFound
		}
		return 0, err
	}
	if _, ok, err := LoadSMTPSettings(db.WithContext(ctx)); err != nil || !ok {
		return 0, ErrEmailNotConfigured
	}
	if a.Kind == AnnouncementMarketing && PublicURL() == "" {
		return 0, ErrNoUnsubscribeLink
	}
	perMinute = min(max(perMinute, 1), 600)
	rs, err := recipients(ctx, db, a.Audience, a.Kind)
	if err != nil {
		return 0, err
	}
	var had []string
	if err := db.WithContext(ctx).Model(&EmailLogEntry{}).Where("template = ?", AnnouncementTemplate(a.ID)).
		Pluck("COALESCE(CAST(user_id AS TEXT), '')", &had).Error; err != nil {
		return 0, err
	}
	done := map[string]bool{}
	for _, u := range had {
		done[u] = true
	}
	todo := rs[:0:0]
	for _, r := range rs {
		if !done[r.ID] {
			todo = append(todo, r)
		}
	}
	if len(todo) == 0 {
		return 0, nil
	}
	gap := time.Minute / time.Duration(perMinute)
	handedOff = true
	go func(ctx context.Context) {
		defer announcementSending.Delete(id)
		start, queued := time.Now(), 0
		for i, r := range todo {
			msg := renderAnnouncement(db.WithContext(ctx), a.Title, a.Body, a.Kind, r)
			if err := queueRendered(ctx, db, AnnouncementTemplate(a.ID), r.Email, msg,
				emailRefs{userID: r.ID, at: start.Add(time.Duration(i) * gap)}); err != nil {
				slog.Default().Warn("announcement: an email could not be queued", "announcement", a.ID, "error", err)
				continue
			}
			queued++
		}
		db.WithContext(ctx).Model(&Announcement{}).Where("id = ?", a.ID).
			Updates(map[string]any{"emailed_at": time.Now().UTC(), "email_queued": gorm.Expr("email_queued + ?", queued)})
	}(context.WithoutCancel(ctx))
	return len(todo), nil
}

// UnsubscribeToken is the signed token in a marketing email's unsubscribe link.
func UnsubscribeToken(userID string) string {
	return userID + "." + unsubscribeMAC(userID)
}

func unsubscribeMAC(userID string) string {
	key, err := middleware.GetJWTSecret()
	if err != nil {
		return ""
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("unsubscribe:" + userID))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// ErrBadUnsubscribeToken is a link that was not made by this panel.
var ErrBadUnsubscribeToken = errors.New("this unsubscribe link is not valid")

// unsubscribeUser is the account a token names, if this panel signed it.
func unsubscribeUser(token string) (string, error) {
	id, mac, ok := strings.Cut(strings.TrimSpace(token), ".")
	want := unsubscribeMAC(id)
	if _, err := uuid.Parse(id); !ok || err != nil || mac == "" || want == "" || !hmac.Equal([]byte(mac), []byte(want)) {
		return "", ErrBadUnsubscribeToken
	}
	return id, nil
}

// Unsubscribe turns marketing email off for the account a token names.
func Unsubscribe(ctx context.Context, db *gorm.DB, token string) error {
	id, err := unsubscribeUser(token)
	if err != nil {
		return err
	}
	return SetMarketingOptOut(ctx, db, id, true)
}

// SetMarketingOptOut records whether an account wants marketing email.
func SetMarketingOptOut(ctx context.Context, db *gorm.DB, userID string, optOut bool) error {
	return db.WithContext(ctx).Table("users").Where("id = ?", userID).Update("marketing_opt_out", optOut).Error
}
