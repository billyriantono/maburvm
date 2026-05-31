package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/maburvm/panel/internal/shared/models"
)

type TemplateRepositoryTestSuite struct {
	BaseTestSuite
	templateRepo *TemplateRepository
}

func (suite *TemplateRepositoryTestSuite) SetupSuite() {
	suite.BaseTestSuite.SetupSuite()
	suite.templateRepo = NewTemplateRepository(suite.DB)
}

func TestTemplateRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(TemplateRepositoryTestSuite))
}

func (suite *TemplateRepositoryTestSuite) TestNewTemplateRepository() {
	assert.NotNil(suite.T(), suite.templateRepo)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_GetByID() {
	ctx := context.Background()

	template := &models.OSTemplate{Name: "Ubuntu", Version: "22.04", ImagePath: "/images/ubuntu-22.04.img", IsActive: true}
	err := suite.DB.Create(template).Error
	assert.NoError(suite.T(), err)

	found, err := suite.templateRepo.GetByID(ctx, template.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), template.Name, found.Name)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_GetByName() {
	ctx := context.Background()

	template := &models.OSTemplate{Name: "Debian", Version: "12", ImagePath: "/images/debian-12.img", IsActive: true}
	err := suite.DB.Create(template).Error
	assert.NoError(suite.T(), err)

	found, err := suite.templateRepo.GetByName(ctx, "Debian")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), template.ID, found.ID)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_List() {
	ctx := context.Background()

	osTypes := []struct{ name, version string }{
		{"Ubuntu", "22.04"}, {"Debian", "12"}, {"CentOS", "Stream9"}, {"Fedora", "40"}, {"Alpine", "3.19"},
	}

	for _, os := range osTypes {
		template := &models.OSTemplate{Name: os.name, Version: os.version, ImagePath: "/images/" + os.name + "-" + os.version + ".img", IsActive: true}
		err := suite.DB.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	templates, err := suite.templateRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), templates, 5)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_ListActive() {
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		template := &models.OSTemplate{Name: "ActiveOS" + string(rune('0'+i)), Version: "1.0", ImagePath: "/images/active" + string(rune('0'+i)) + ".img", IsActive: true}
		err := suite.DB.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	for i := 0; i < 2; i++ {
		template := &models.OSTemplate{Name: "InactiveOS" + string(rune('0'+i)), Version: "1.0", ImagePath: "/images/inactive" + string(rune('0'+i)) + ".img", IsActive: true}
		err := suite.DB.Create(template).Error
		assert.NoError(suite.T(), err)
		err = suite.templateRepo.UpdateActiveStatus(ctx, template.ID, false)
		assert.NoError(suite.T(), err)
	}

	activeTemplates, err := suite.templateRepo.ListActive(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), activeTemplates, 3)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_Create() {
	ctx := context.Background()

	template := &models.OSTemplate{Name: "Rocky Linux", Version: "9.3", ImagePath: "/images/rocky-9.3.img", IsActive: true, Description: "Enterprise Linux"}
	err := suite.templateRepo.Create(ctx, template)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), template.ID)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_Update() {
	ctx := context.Background()

	template := &models.OSTemplate{Name: "Fedora", Version: "39", ImagePath: "/images/fedora-39.img", IsActive: true}
	err := suite.DB.Create(template).Error
	assert.NoError(suite.T(), err)

	template.Version = "40"
	err = suite.templateRepo.Update(ctx, template)
	assert.NoError(suite.T(), err)

	var found models.OSTemplate
	err = suite.DB.First(&found, "id = ?", template.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "40", found.Version)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_Delete() {
	ctx := context.Background()

	template := &models.OSTemplate{Name: "Arch Linux", Version: "2024.01", ImagePath: "/images/arch-2024.01.img", IsActive: true}
	err := suite.DB.Create(template).Error
	assert.NoError(suite.T(), err)

	err = suite.templateRepo.Delete(ctx, template.ID)
	assert.NoError(suite.T(), err)

	var count int64
	suite.DB.Unscoped().Model(&models.OSTemplate{}).Where("id = ?", template.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_Count() {
	ctx := context.Background()

	count, err := suite.templateRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	for i := 0; i < 5; i++ {
		template := &models.OSTemplate{Name: "CountOS" + string(rune('0'+i)), Version: "1.0", ImagePath: "/images/count" + string(rune('0'+i)) + ".img", IsActive: true}
		err := suite.DB.Create(template).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.templateRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_NameExists() {
	ctx := context.Background()

	exists, err := suite.templateRepo.NameExists(ctx, "NonExistent")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)

	template := &models.OSTemplate{Name: "UniqueName", Version: "1.0", ImagePath: "/images/unique.img", IsActive: true}
	err = suite.DB.Create(template).Error
	assert.NoError(suite.T(), err)

	exists, err = suite.templateRepo.NameExists(ctx, "UniqueName")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)
}

func (suite *TemplateRepositoryTestSuite) TestTemplateRepository_UpdateActiveStatus() {
	ctx := context.Background()

	template := &models.OSTemplate{Name: "ToggleOS", Version: "1.0", ImagePath: "/images/toggle.img", IsActive: true}
	err := suite.DB.Create(template).Error
	assert.NoError(suite.T(), err)

	err = suite.templateRepo.UpdateActiveStatus(ctx, template.ID, false)
	assert.NoError(suite.T(), err)

	var found models.OSTemplate
	err = suite.DB.First(&found, "id = ?", template.ID).Error
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), found.IsActive)
}
