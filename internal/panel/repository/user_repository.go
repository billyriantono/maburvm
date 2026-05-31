package repository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// UserRepository provides data access for users
type UserRepository struct {
	base *BaseRepository[models.User]
	db   *gorm.DB
}

// NewUserRepository creates a new UserRepository instance
func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{
		base: NewBaseRepository[models.User](db),
		db:   db,
	}
}

// GetByID retrieves a user by ID
func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	return r.base.GetByID(ctx, id)
}

// GetByEmail retrieves a user by email address
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*models.User, error) {
	var user models.User
	if err := r.db.WithContext(ctx).Where("email = ?", email).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// List retrieves all users with optional pagination
func (r *UserRepository) List(ctx context.Context, limit, offset int) ([]models.User, error) {
	return r.base.List(ctx, limit, offset)
}

// ListByRole retrieves users filtered by role with optional pagination
func (r *UserRepository) ListByRole(ctx context.Context, role models.UserRole, limit, offset int) ([]models.User, error) {
	var users []models.User
	query := r.db.WithContext(ctx).Where("role = ?", role)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// Create inserts a new user
func (r *UserRepository) Create(ctx context.Context, user *models.User) error {
	return r.base.Create(ctx, user)
}

// Update updates an existing user
func (r *UserRepository) Update(ctx context.Context, user *models.User) error {
	return r.base.Update(ctx, user)
}

// Delete removes a user by ID (hard delete as per PRD compliance requirements)
func (r *UserRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.base.Delete(ctx, id)
}

// Count returns the total number of users
func (r *UserRepository) Count(ctx context.Context) (int64, error) {
	return r.base.Count(ctx)
}

// CountByRole returns the number of users with a specific role
func (r *UserRepository) CountByRole(ctx context.Context, role models.UserRole) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.User{}).Where("role = ?", role).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// EmailExists checks if an email address is already registered
func (r *UserRepository) EmailExists(ctx context.Context, email string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.User{}).Where("email = ?", email).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// UpdatePassword updates a user's password hash
func (r *UserRepository) UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string) error {
	return r.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", id).Update("password_hash", passwordHash).Error
}

// UpdateTwoFactorSecret updates a user's 2FA secret
func (r *UserRepository) UpdateTwoFactorSecret(ctx context.Context, id uuid.UUID, secret string) error {
	return r.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", id).Update("two_factor_secret", secret).Error
}

// ClearTwoFactorSecret removes a user's 2FA secret
func (r *UserRepository) ClearTwoFactorSecret(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", id).Update("two_factor_secret", "").Error
}

// UpdateIPWhitelist updates a user's IP whitelist
func (r *UserRepository) UpdateIPWhitelist(ctx context.Context, id uuid.UUID, whitelist []string) error {
	return r.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", id).Select("IPWhitelist").Updates(&models.User{IPWhitelist: whitelist}).Error
}
