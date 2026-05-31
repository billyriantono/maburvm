package repository

import (
	"context"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// DNSRepository provides data access for DNS zones and records.
type DNSRepository struct {
	db *gorm.DB
}

// NewDNSRepository creates a new DNSRepository.
func NewDNSRepository(db *gorm.DB) *DNSRepository {
	return &DNSRepository{db: db}
}

// --- Zones ---

func (r *DNSRepository) CreateZone(ctx context.Context, zone *models.DNSZone) error {
	return r.db.WithContext(ctx).Create(zone).Error
}

func (r *DNSRepository) ListZones(ctx context.Context) ([]models.DNSZone, error) {
	var zones []models.DNSZone
	return zones, r.db.WithContext(ctx).Order("name ASC").Find(&zones).Error
}

func (r *DNSRepository) GetZone(ctx context.Context, id string) (*models.DNSZone, error) {
	var zone models.DNSZone
	if err := r.db.WithContext(ctx).First(&zone, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &zone, nil
}

func (r *DNSRepository) UpdateZone(ctx context.Context, zone *models.DNSZone) error {
	return r.db.WithContext(ctx).Save(zone).Error
}

func (r *DNSRepository) DeleteZone(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.DNSZone{}).Error
}

func (r *DNSRepository) ZoneNameExists(ctx context.Context, name string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.DNSZone{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// --- Records ---

func (r *DNSRepository) CreateRecord(ctx context.Context, record *models.DNSRecord) error {
	return r.db.WithContext(ctx).Create(record).Error
}

func (r *DNSRepository) ListRecords(ctx context.Context, zoneID string) ([]models.DNSRecord, error) {
	var records []models.DNSRecord
	return records, r.db.WithContext(ctx).Where("zone_id = ?", zoneID).Order("type ASC, name ASC").Find(&records).Error
}

func (r *DNSRepository) GetRecord(ctx context.Context, id string) (*models.DNSRecord, error) {
	var record models.DNSRecord
	if err := r.db.WithContext(ctx).First(&record, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &record, nil
}

func (r *DNSRepository) UpdateRecord(ctx context.Context, record *models.DNSRecord) error {
	return r.db.WithContext(ctx).Save(record).Error
}

func (r *DNSRepository) DeleteRecord(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.DNSRecord{}).Error
}
