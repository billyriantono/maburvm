package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrBillingUserInvalid is a user.ensure request the panel cannot act on.
	ErrBillingUserInvalid = errors.New("invalid customer")
	// ErrBillingStaffAccount refuses to hand a staff account to the billing system.
	ErrBillingStaffAccount = errors.New("that email belongs to a staff account of the panel; a billing order needs a customer account")
)

// newAccountLinkTTL is how long the "choose a password" link of a new
// billing-created account works. Longer than a reset: nobody asked for it.
const newAccountLinkTTL = 72 * time.Hour

// billingUserLink ties the billing system's reference for a customer to a
// panel account.
type billingUserLink struct {
	ExternalRef string    `gorm:"column:external_ref;primaryKey"`
	UserID      string    `gorm:"column:user_id"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (billingUserLink) TableName() string { return "billing_user_links" }

// BillingUserRef returns the billing system's reference for an account, or "".
func BillingUserRef(ctx context.Context, db *gorm.DB, userID string) string {
	var ref string
	_ = db.WithContext(ctx).Raw("SELECT external_ref FROM billing_user_links WHERE user_id = ? ORDER BY created_at LIMIT 1", userID).Scan(&ref).Error
	return ref
}

// EnsureBillingUser finds the customer account for a billing client, or
// creates one. The billing system's own reference is tried first, so a client
// who changed their email keeps their account; then the email. created reports
// a new account, which has no usable password until its owner chooses one.
func EnsureBillingUser(ctx context.Context, db *gorm.DB, email, name, externalRef string) (user *models.User, created bool, err error) {
	email = strings.ToLower(strings.TrimSpace(email))
	externalRef = strings.TrimSpace(externalRef)
	name = strings.TrimSpace(name)
	if addr, perr := mail.ParseAddress(email); perr != nil || addr.Address != email {
		return nil, false, fmt.Errorf("%w: email is not a valid address", ErrBillingUserInvalid)
	}
	if len(email) > 255 || len(name) > 255 || len(externalRef) > 255 {
		return nil, false, fmt.Errorf("%w: email, name and external_ref are at most 255 characters", ErrBillingUserInvalid)
	}

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var found models.User
		if externalRef != "" {
			lerr := tx.Where("id IN (SELECT user_id FROM billing_user_links WHERE external_ref = ?)", externalRef).First(&found).Error
			if lerr == nil {
				user = &found
				return checkBillingCustomer(user)
			}
			if !errors.Is(lerr, gorm.ErrRecordNotFound) {
				return lerr
			}
		}
		ferr := tx.Where("LOWER(email) = ?", email).First(&found).Error
		switch {
		case ferr == nil:
			user = &found
			if err := checkBillingCustomer(user); err != nil {
				return err
			}
		case errors.Is(ferr, gorm.ErrRecordNotFound):
			// Nobody knows this password: the owner chooses theirs from the email.
			secret := make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return err
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(hex.EncodeToString(secret)), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			user = &models.User{Name: name, Email: email, PasswordHash: string(hash), Role: models.RoleClient}
			if err := repository.NewUserRepository(tx).Create(ctx, user); err != nil {
				return fmt.Errorf("could not create the account (an account with this email may have been removed from the panel): %w", err)
			}
			// As for an account staff create: one left without a quota row may order without limit.
			if err := ApplyNewClientQuota(ctx, tx, user.ID.String()); err != nil {
				return err
			}
			created = true
		default:
			return ferr
		}
		if externalRef == "" {
			return nil
		}
		// The reference moves with the client: a stale link to a removed account is replaced.
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "external_ref"}},
			DoUpdates: clause.Assignments(map[string]interface{}{"user_id": user.ID.String()}),
		}).Create(&billingUserLink{ExternalRef: externalRef, UserID: user.ID.String(), CreatedAt: time.Now().UTC()}).Error
	})
	if err != nil {
		return nil, false, err
	}
	if created {
		sendNewBillingAccountEmail(ctx, db, user)
	}
	return user, created, nil
}

func checkBillingCustomer(u *models.User) error {
	if u.Role != models.RoleClient {
		return ErrBillingStaffAccount
	}
	return nil
}

// deliverNewAccountLink emails the "choose a password" link. A var so tests
// can capture the link.
var deliverNewAccountLink = func(db *gorm.DB, to, name, link string) {
	cfg, ok, err := LoadSMTPSettings(db)
	if err != nil || !ok || !cfg.Enabled(EmailWelcomeOrder) {
		return
	}
	panelURL := PublicURL()
	// Through the queue: a mail server that is slow to answer must not time out a paid order.
	if err := queueRendered(context.Background(), db, EmailWelcomeOrder, to, RenderEmail(cfg.Template(EmailWelcomeOrder), cfg.Brand,
		EmailData{Name: name, Email: to, Link: link, LogoURL: EmailLogoURL(cfg.Brand, panelURL)}), emailRefs{}); err != nil {
		slog.Default().Warn("billing: could not email the new account's owner", "email", to, "error", err)
	}
}

// sendNewBillingAccountEmail tells a new customer their account exists and
// lets them choose its password. Best-effort: without it they use "Forgot
// password" on the sign-in page.
func sendNewBillingAccountEmail(ctx context.Context, db *gorm.DB, user *models.User) {
	base := PublicURL()
	if base == "" {
		slog.Default().Warn("billing: the panel address is not set (System settings, Panel, or PANEL_PUBLIC_URL), so the new account's owner was not emailed a link to choose a password", "email", user.Email)
		return
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return
	}
	raw := hex.EncodeToString(buf)
	tok := &models.PasswordResetToken{ID: uuid.New(), UserID: user.ID, TokenHash: hashResetToken(raw), ExpiresAt: time.Now().Add(newAccountLinkTTL)}
	if err := repository.NewEnrollmentRepository(db).CreatePasswordResetToken(ctx, tok); err != nil {
		slog.Default().Warn("billing: could not issue the new account's password link", "email", user.Email, "error", err)
		return
	}
	deliverNewAccountLink(db, user.Email, user.Name, base+"/reset-password?token="+raw)
}
