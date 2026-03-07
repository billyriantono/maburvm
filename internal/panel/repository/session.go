package repository

import (
	"time"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// SessionRepository provides data access for user sessions
type SessionRepository struct {
	db *gorm.DB
}

// NewSessionRepository creates a new SessionRepository instance
func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

// Create inserts a new session
func (r *SessionRepository) Create(s *models.Session) error {
	return r.db.Create(s).Error
}

// GetByID retrieves a session by ID
func (r *SessionRepository) GetByID(id string) (*models.Session, error) {
	var session models.Session
	if err := r.db.First(&session, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

// GetByToken retrieves a session by token
func (r *SessionRepository) GetByToken(token string) (*models.Session, error) {
	var session models.Session
	if err := r.db.First(&session, "token = ?", token).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

// GetByUserID retrieves all sessions for a user
func (r *SessionRepository) GetByUserID(userID string) ([]models.Session, error) {
	var sessions []models.Session
	if err := r.db.Where("user_id = ?", userID).Find(&sessions).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

// Update updates an existing session
func (r *SessionRepository) Update(s *models.Session) error {
	return r.db.Save(s).Error
}

// Delete removes a session by ID
func (r *SessionRepository) Delete(id string) error {
	return r.db.Delete(&models.Session{}, "id = ?", id).Error
}

// DeleteByUserID removes all sessions for a user (logout-all functionality)
func (r *SessionRepository) DeleteByUserID(userID string) error {
	return r.db.Delete(&models.Session{}, "user_id = ?", userID).Error
}

// DeleteExpired removes all expired sessions
func (r *SessionRepository) DeleteExpired() error {
	return r.db.Delete(&models.Session{}, "expires_at < ?", time.Now()).Error
}

// List retrieves all sessions with pagination
func (r *SessionRepository) List(limit, offset int) ([]models.Session, error) {
	var sessions []models.Session
	query := r.db
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&sessions).Error; err != nil {
		return nil, err
	}
	return sessions, nil
}

// Count returns the total number of sessions
func (r *SessionRepository) Count() (int64, error) {
	var count int64
	if err := r.db.Model(&models.Session{}).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CountByUserID returns the number of active sessions for a user
func (r *SessionRepository) CountByUserID(userID string) (int64, error) {
	var count int64
	if err := r.db.Model(&models.Session{}).Where("user_id = ? AND expires_at > ?", userID, time.Now()).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// IsValidToken checks if a token is valid and not expired
func (r *SessionRepository) IsValidToken(token string) (bool, error) {
	var count int64
	if err := r.db.Model(&models.Session{}).Where("token = ? AND expires_at > ?", token, time.Now()).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
