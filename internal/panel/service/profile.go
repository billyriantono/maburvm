package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ProfileInput is what an account holder may change about themselves. A nil
// field is left as it is.
type ProfileInput struct {
	Name     *string `json:"name"`
	Phone    *string `json:"phone"`
	Company  *string `json:"company"`
	Timezone *string `json:"timezone"`
	Locale   *string `json:"locale"`
}

var (
	profilePhone  = regexp.MustCompile(`^[0-9+()\-. ]{0,40}$`)
	profileLocale = regexp.MustCompile(`^([a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})*)?$`)
)

// columns checks the input and returns the columns to write.
func (in ProfileInput) columns() (map[string]any, error) {
	cols := map[string]any{}
	trim := func(p *string) string { return strings.Join(strings.Fields(*p), " ") }
	if in.Name != nil {
		v := trim(in.Name)
		if len(v) > 255 {
			return nil, fmt.Errorf("the name can be up to 255 characters")
		}
		cols["name"] = v
	}
	if in.Phone != nil {
		v := strings.TrimSpace(*in.Phone)
		if !profilePhone.MatchString(v) {
			return nil, fmt.Errorf("the phone number can hold digits, spaces and + ( ) - . only, up to 40")
		}
		cols["phone"] = v
	}
	if in.Company != nil {
		v := trim(in.Company)
		if len(v) > 255 {
			return nil, fmt.Errorf("the company can be up to 255 characters")
		}
		cols["company"] = v
	}
	if in.Timezone != nil {
		v := strings.TrimSpace(*in.Timezone)
		if v != "" {
			if _, err := time.LoadLocation(v); err != nil || len(v) > 64 || v == "Local" {
				return nil, fmt.Errorf("%q is not a time zone; use a name such as Asia/Jakarta or UTC", v)
			}
		}
		cols["timezone"] = v
	}
	if in.Locale != nil {
		v := strings.TrimSpace(*in.Locale)
		if len(v) > 16 || !profileLocale.MatchString(v) {
			return nil, fmt.Errorf("%q is not a language code; use one such as en or id-ID", v)
		}
		cols["locale"] = v
	}
	return cols, nil
}

// UpdateOwnProfile writes the fields given. The email address is not among
// them: changing it needs the password (see PUT /settings/profile).
func UpdateOwnProfile(ctx context.Context, db *gorm.DB, userID string, in ProfileInput) error {
	cols, err := in.columns()
	if err != nil || len(cols) == 0 {
		return err
	}
	cols["updated_at"] = time.Now().UTC()
	return db.WithContext(ctx).Table("users").Where("id = ? AND deleted_at IS NULL", userID).Updates(cols).Error
}

// NotificationsPage is one page of an account's notifications, newest first,
// with how many there are and how many are unread.
func NotificationsPage(ctx context.Context, db *gorm.DB, userID string, limit, offset int) ([]UserNotification, int64, int64, error) {
	rows, unread, err := []UserNotification{}, int64(0), error(nil)
	if err = db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, 0, err
	}
	var total int64
	if err = db.WithContext(ctx).Model(&UserNotification{}).Where("user_id = ?", userID).Count(&total).Error; err != nil {
		return nil, 0, 0, err
	}
	err = db.WithContext(ctx).Model(&UserNotification{}).Where("user_id = ? AND read_at IS NULL", userID).Count(&unread).Error
	return rows, total, unread, err
}
