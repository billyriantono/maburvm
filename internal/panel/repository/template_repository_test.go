package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/shared/models"
)

// TemplateRepositoryTestSuite tests TemplateRepository
type TemplateRepositoryTestSuite struct {
	suite.Suite
	db           *gorm.DB
	templateRepo *TemplateRepository
}

func (suite *TemplateRepositoryTestSuite) SetupSuite() {
	var err error
	suite.db, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		suite.T().Fatalf("Failed to connect to test database: %v", err)
	}

	err = suite.db.AutoMigrate(&models.OSTemplate{})
	if err != nil {
		suite.T().Fatalf("Failed to migrate: %v", err)
	}

	suite.templateRepo = NewTemplateRepository(suite.db)
}

func (suite *TemplateRepositoryTestSuite) SetupTest() {
	suite.db.Exec("DELETE FROM os_templates")
}

func (suite *TemplateRepositoryTestSuite) TearDownSuite() {
	sqlDB, err := suite.db.DB()
	if err == nil {
		sqlDB.Close()
	}
}

func TestTemplateRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(TemplateRepositoryTestSuite))
}

func (suite *TemplateRepositoryTestSuite) TestNewTemplateRepository() {
	assert.NotNil(suite.T(), suite.templateRepo)
	assert.NotNil(suite.T(), suite.templateRepo.base)
	assert.NotNil(suite.T(), suite.templateRepo.db)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_GetByID() {
	ctx := context.Background()

	// Create template
	template := &models.OSTemplate{
		Name:      "Ubuntu",
		Version:   "22.04",
		ImagePath: "/images/ubuntu-22.04.img",
		IsActive:  true,
	}
	err := suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	// Get by ID
	found, err := suite.templateRepo.GetByID(ctx, template.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), template.Name, found.Name)
	assert.Equal(suite.T(), template.Version, found.Version)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_GetByName() {
	ctx := context.Background()

	// Create template
	template := &models.OSTemplate{
		Name:      "Debian",
		Version:   "12",
		ImagePath: "/images/debian-12.img",
		IsActive:  true,
	}
	err := suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	// Get by name
	found, err := suite.templateRepo.GetByName(ctx, "Debian")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), template.ID, found.ID)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_GetByName_NotFound() {
	ctx := context.Background()

	_, err := suite.templateRepo.GetByName(ctx, "NonExistent")
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), gorm.ErrRecordNotFound, err)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_GetByNameAndVersion() {
	ctx := context.Background()

	// Create multiple versions
	for _, version := range []string{"20.04", "22.04", "24.04"} {
		template := &models.OSTemplate{
			Name:      "Ubuntu",
			Version:   version,
			ImagePath: "/images/ubuntu-" + version + ".img",
			IsActive:  true,
		}
		err := suite.db.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	// Get specific version
	found, err := suite.templateRepo.GetByNameAndVersion(ctx, "Ubuntu", "22.04")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "Ubuntu", found.Name)
	assert.Equal(suite.T(), "22.04", found.Version)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_GetByImagePath() {
	ctx := context.Background()

	// Create template
	template := &models.OSTemplate{
		Name:      "CentOS",
		Version:   "Stream9",
		ImagePath: "/images/centos-stream9.img",
		IsActive:  true,
	}
	err := suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	// Get by image path
	found, err := suite.templateRepo.GetByImagePath(ctx, "/images/centos-stream9.img")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), template.ID, found.ID)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_List() {
	ctx := context.Background()

	// Create templates
	osTypes := []struct {
		name    string
		version string
	}{
		{"Ubuntu", "22.04"},
		{"Debian", "12"},
		{"CentOS", "Stream9"},
		{"Fedora", "40"},
		{"Alpine", "3.19"},
	}

	for _, os := range osTypes {
		template := &models.OSTemplate{
			Name:      os.name,
			Version:   os.version,
			ImagePath: "/images/" + os.name + "-" + os.version + ".img",
			IsActive:  true,
		}
		err := suite.db.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	// List all
	templates, err := suite.templateRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), templates, 5)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_ListActive() {
	ctx := context.Background()

	// Create active templates
	for i := 0; i < 3; i++ {
		template := &models.OSTemplate{
			Name:      "ActiveOS" + string(rune('0'+i)),
			Version:   "1.0",
			ImagePath: "/images/active" + string(rune('0'+i)) + ".img",
			IsActive:  true,
		}
		err := suite.db.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	// Create inactive templates
	for i := 0; i < 2; i++ {
		template := &models.OSTemplate{
			Name:      "InactiveOS" + string(rune('0'+i)),
			Version:   "1.0",
			ImagePath: "/images/inactive" + string(rune('0'+i)) + ".img",
			IsActive:  false,
		}
		err := suite.db.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	// List active
	activeTemplates, err := suite.templateRepo.ListActive(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), activeTemplates, 3)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_ListInactive() {
	ctx := context.Background()

	// Create active templates
	for i := 0; i < 3; i++ {
		template := &models.OSTemplate{
			Name:      "ActiveOS" + string(rune('0'+i)),
			Version:   "1.0",
			ImagePath: "/images/active" + string(rune('0'+i)) + ".img",
			IsActive:  true,
		}
		err := suite.db.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	// Create inactive templates
	for i := 0; i < 2; i++ {
		template := &models.OSTemplate{
			Name:      "InactiveOS" + string(rune('0'+i)),
			Version:   "1.0",
			ImagePath: "/images/inactive" + string(rune('0'+i)) + ".img",
			IsActive:  false,
		}
		err := suite.db.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	// List inactive
	inactiveTemplates, err := suite.templateRepo.ListInactive(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), inactiveTemplates, 2)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_Create() {
	ctx := context.Background()

	template := &models.OSTemplate{
		Name:        "Rocky Linux",
		Version:     "9.3",
		ImagePath:   "/images/rocky-9.3.img",
		IsActive:    true,
		Description: "Enterprise Linux distribution",
	}

	err := suite.templateRepo.Create(ctx, template)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), template.ID)

	// Verify
	var found models.OSTemplate
	err = suite.db.First(&found, template.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), template.Name, found.Name)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_Update() {
	ctx := context.Background()

	// Create template
	template := &models.OSTemplate{
		Name:      "Fedora",
		Version:   "39",
		ImagePath: "/images/fedora-39.img",
		IsActive:  true,
	}
	err := suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	// Update
	template.Version = "40"
	err = suite.templateRepo.Update(ctx, template)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.OSTemplate
	err = suite.db.First(&found, template.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "40", found.Version)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_Delete() {
	ctx := context.Background()

	// Create template
	template := &models.OSTemplate{
		Name:      "Arch Linux",
		Version:   "2024.01",
		ImagePath: "/images/arch-2024.01.img",
		IsActive:  true,
	}
	err := suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	// Delete
	err = suite.templateRepo.Delete(ctx, template.ID)
	assert.NoError(suite.T(), err)

	// Verify hard delete
	var count int64
	suite.db.Unscoped().Model(&models.OSTemplate{}).Where("id = ?", template.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_Count() {
	ctx := context.Background()

	// Initial count
	count, err := suite.templateRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	// Create templates
	for i := 0; i < 5; i++ {
		template := &models.OSTemplate{
			Name:      "CountOS" + string(rune('0'+i)),
			Version:   "1.0",
			ImagePath: "/images/count" + string(rune('0'+i)) + ".img",
			IsActive:  true,
		}
		err := suite.db.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.templateRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_CountActive() {
	ctx := context.Background()

	// Create active templates
	for i := 0; i < 4; i++ {
		template := &models.OSTemplate{
			Name:      "ActiveCount" + string(rune('0'+i)),
			Version:   "1.0",
			ImagePath: "/images/active-count" + string(rune('0'+i)) + ".img",
			IsActive:  true,
		}
		err := suite.db.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	// Create inactive templates
	for i := 0; i < 2; i++ {
		template := &models.OSTemplate{
			Name:      "InactiveCount" + string(rune('0'+i)),
			Version:   "1.0",
			ImagePath: "/images/inactive-count" + string(rune('0'+i)) + ".img",
			IsActive:  false,
		}
		err := suite.db.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	// Count active
	count, err := suite.templateRepo.CountActive(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(4), count)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_UpdateActiveStatus() {
	ctx := context.Background()

	// Create template
	template := &models.OSTemplate{
		Name:      "ToggleOS",
		Version:   "1.0",
		ImagePath: "/images/toggle.img",
		IsActive:  true,
	}
	err := suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	// Deactivate
	err = suite.templateRepo.UpdateActiveStatus(ctx, template.ID, false)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.OSTemplate
	err = suite.db.First(&found, template.ID).Error
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), found.IsActive)

	// Reactivate
	err = suite.templateRepo.UpdateActiveStatus(ctx, template.ID, true)
	assert.NoError(suite.T(), err)

	// Verify
	err = suite.db.First(&found, template.ID).Error
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), found.IsActive)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_UpdateImagePath() {
	ctx := context.Background()

	// Create template
	template := &models.OSTemplate{
		Name:      "MoveOS",
		Version:   "1.0",
		ImagePath: "/old/path/image.img",
		IsActive:  true,
	}
	err := suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	// Update image path
	newPath := "/new/path/image.img"
	err = suite.templateRepo.UpdateImagePath(ctx, template.ID, newPath)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.OSTemplate
	err = suite.db.First(&found, template.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), newPath, found.ImagePath)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_UpdateVersion() {
	ctx := context.Background()

	// Create template
	template := &models.OSTemplate{
		Name:      "VersionOS",
		Version:   "1.0",
		ImagePath: "/images/version.img",
		IsActive:  true,
	}
	err := suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	// Update version
	err = suite.templateRepo.UpdateVersion(ctx, template.ID, "2.0")
	assert.NoError(suite.T(), err)

	// Verify
	var found models.OSTemplate
	err = suite.db.First(&found, template.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "2.0", found.Version)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_NameExists() {
	ctx := context.Background()

	// Check non-existent name
	exists, err := suite.templateRepo.NameExists(ctx, "NonExistent")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)

	// Create template
	template := &models.OSTemplate{
		Name:      "UniqueName",
		Version:   "1.0",
		ImagePath: "/images/unique.img",
		IsActive:  true,
	}
	err = suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	// Check existing name
	exists, err = suite.templateRepo.NameExists(ctx, "UniqueName")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_NameAndVersionExists() {
	ctx := context.Background()

	// Check non-existent combination
	exists, err := suite.templateRepo.NameAndVersionExists(ctx, "NonExistent", "1.0")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)

	// Create template
	template := &models.OSTemplate{
		Name:      "ComboOS",
		Version:   "2.5",
		ImagePath: "/images/combo.img",
		IsActive:  true,
	}
	err = suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	// Check existing combination
	exists, err = suite.templateRepo.NameAndVersionExists(ctx, "ComboOS", "2.5")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)

	// Check non-matching version
	exists, err = suite.templateRepo.NameAndVersionExists(ctx, "ComboOS", "1.0")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_ImagePathExists() {
	ctx := context.Background()

	// Check non-existent path
	exists, err := suite.templateRepo.ImagePathExists(ctx, "/nonexistent/path.img")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)

	// Create template
	template := &models.OSTemplate{
		Name:      "PathOS",
		Version:   "1.0",
		ImagePath: "/images/exists.img",
		IsActive:  true,
	}
	err = suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	// Check existing path
	exists, err = suite.templateRepo.ImagePathExists(ctx, "/images/exists.img")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_GetIDs() {
	ctx := context.Background()

	// Create templates
	for i := 0; i < 5; i++ {
		template := &models.OSTemplate{
			Name:      "IDOS" + string(rune('0'+i)),
			Version:   "1.0",
			ImagePath: "/images/id" + string(rune('0'+i)) + ".img",
			IsActive:  true,
		}
		err := suite.db.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	// Get all IDs
	ids, err := suite.templateRepo.GetIDs(ctx)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), ids, 5)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_GetIDsByActiveStatus() {
	ctx := context.Background()

	// Create active templates
	for i := 0; i < 3; i++ {
		template := &models.OSTemplate{
			Name:      "ActiveIDOS" + string(rune('0'+i)),
			Version:   "1.0",
			ImagePath: "/images/active-id" + string(rune('0'+i)) + ".img",
			IsActive:  true,
		}
		err := suite.db.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	// Create inactive templates
	for i := 0; i < 2; i++ {
		template := &models.OSTemplate{
			Name:      "InactiveIDOS" + string(rune('0'+i)),
			Version:   "1.0",
			ImagePath: "/images/inactive-id" + string(rune('0'+i)) + ".img",
			IsActive:  false,
		}
		err := suite.db.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	// Get active IDs
	activeIDs, err := suite.templateRepo.GetIDsByActiveStatus(ctx, true)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), activeIDs, 3)

	// Get inactive IDs
	inactiveIDs, err := suite.templateRepo.GetIDsByActiveStatus(ctx, false)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), inactiveIDs, 2)
}
