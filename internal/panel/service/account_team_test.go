package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// teamTestDB is a sqlite database with the tables teams touch.
func teamTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:team-%s?mode=memory&cache=shared", uuid.NewString())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, ddl := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', email TEXT UNIQUE NOT NULL, password_hash TEXT NOT NULL,
			role TEXT DEFAULT 'client', quota_mode TEXT NOT NULL DEFAULT 'legacy', two_factor_secret TEXT, two_factor_enabled BOOLEAN NOT NULL DEFAULT 0,
			two_factor_backup_codes TEXT, ip_whitelist TEXT, created_at DATETIME, updated_at DATETIME, token_revoked_at DATETIME, deleted_at DATETIME,
			suspended_at DATETIME, member_only BOOLEAN NOT NULL DEFAULT 0, phone TEXT NOT NULL DEFAULT '', company TEXT NOT NULL DEFAULT '',
			timezone TEXT NOT NULL DEFAULT '', locale TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, user_id TEXT, node_id TEXT, hostname TEXT, status TEXT, suspended BOOLEAN NOT NULL DEFAULT 0,
			plan_id TEXT, project_id TEXT, deleted_at DATETIME, created_at DATETIME)`,
		`CREATE TABLE account_members (id TEXT PRIMARY KEY, account_id TEXT NOT NULL, user_id TEXT, email TEXT NOT NULL, role TEXT NOT NULL,
			vm_ids TEXT NOT NULL DEFAULT '[]', project_ids TEXT NOT NULL DEFAULT '[]', invite_token_hash TEXT, invite_expires_at DATETIME,
			invited_by TEXT, accepted_at DATETIME, created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE projects (id TEXT PRIMARY KEY, account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
			created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`,
		`CREATE TABLE user_quotas (user_id TEXT PRIMARY KEY, max_vms INTEGER DEFAULT 0, max_vcpu INTEGER DEFAULT 0, max_ram_mb INTEGER DEFAULT 0,
			max_disk_gb INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			quota_mode TEXT NOT NULL DEFAULT 'legacy', policy_id TEXT, policy_version INTEGER, policy_name TEXT, policy_assigned_at DATETIME,
			policy_assigned_by TEXT, cap_revision_id TEXT)`,
	} {
		require.NoError(t, db.Exec(ddl).Error)
	}
	return db
}

func teamUser(t *testing.T, db *gorm.DB, email string) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO users (id, email, password_hash, role, created_at, updated_at) VALUES (?, ?, 'x', 'client', ?, ?)`,
		id, email, time.Now(), time.Now()).Error)
	return id
}

func teamVM(t *testing.T, db *gorm.DB, owner string, project *string) *models.VM {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO vms (id, user_id, hostname, status, project_id, created_at) VALUES (?, ?, ?, 'running', ?, ?)`,
		id, owner, "vm-"+id[:6], project, time.Now()).Error)
	return &models.VM{ID: id, UserID: owner, Status: models.VMStatusRunning, ProjectID: project}
}

// member adds an accepted membership straight into the table.
func member(t *testing.T, db *gorm.DB, account, user, role string, vmIDs, projectIDs []string) string {
	t.Helper()
	id := uuid.NewString()
	now := time.Now()
	require.NoError(t, db.Create(&AccountMember{ID: id, AccountID: account, UserID: &user, Email: user + "@example.com", Role: role,
		VMIDs: nonNil(vmIDs), ProjectIDs: nonNil(projectIDs), AcceptedAt: &now, CreatedAt: now, UpdatedAt: now}).Error)
	return id
}

func TestCheckVMAccessThroughMembership(t *testing.T) {
	db := teamTestDB(t)
	ctx := context.Background()
	owner := teamUser(t, db, "owner@example.com")
	stranger := teamUser(t, db, "stranger@example.com")
	viewer := teamUser(t, db, "viewer@example.com")
	tech := teamUser(t, db, "tech@example.com")
	scoped := teamUser(t, db, "scoped@example.com")
	byProject := teamUser(t, db, "project@example.com")
	invited := teamUser(t, db, "invited@example.com")

	project := uuid.NewString()
	require.NoError(t, db.Create(&Project{ID: project, AccountID: owner, Name: "web"}).Error)
	vm := teamVM(t, db, owner, nil)
	other := teamVM(t, db, owner, &project)

	member(t, db, owner, viewer, MemberRoleReadOnly, nil, nil)
	member(t, db, owner, tech, MemberRoleTechnical, nil, nil)
	member(t, db, owner, scoped, MemberRoleTechnical, []string{vm.ID}, nil)
	member(t, db, owner, byProject, MemberRoleAdmin, nil, []string{project})
	// An invitation not yet accepted lets nobody in.
	require.NoError(t, db.Create(&AccountMember{ID: uuid.NewString(), AccountID: owner, UserID: &invited, Email: "invited@example.com",
		Role: MemberRoleAdmin, VMIDs: []string{}, ProjectIDs: []string{}}).Error)

	for _, c := range []struct {
		name string
		vm   *models.VM
		user string
		need AccessNeed
		want error
	}{
		{"owner changes", vm, owner, AccessChange, nil},
		{"stranger reads", vm, stranger, AccessRead, ErrVMNotFound},
		{"viewer reads", vm, viewer, AccessRead, nil},
		{"viewer changes", vm, viewer, AccessChange, ErrMemberReadOnly},
		{"viewer opens the console", vm, viewer, AccessOperate, ErrMemberReadOnly},
		{"technical changes", vm, tech, AccessChange, nil},
		{"scoped reaches its VM", vm, scoped, AccessChange, nil},
		{"scoped does not reach another", other, scoped, AccessRead, ErrVMNotFound},
		{"project member reaches the project's VM", other, byProject, AccessChange, nil},
		{"project member does not reach a VM in no project", vm, byProject, AccessRead, ErrVMNotFound},
		{"open invitation reaches nothing", vm, invited, AccessRead, ErrVMNotFound},
	} {
		err := CheckVMAccess(ctx, db, c.vm, c.user, false, c.need)
		if c.want == nil {
			require.NoError(t, err, c.name)
		} else {
			require.ErrorIs(t, err, c.want, c.name)
		}
	}

	// Billing sees usage and billing, not the servers.
	biller := teamUser(t, db, "biller@example.com")
	member(t, db, owner, biller, MemberRoleBilling, nil, nil)
	require.ErrorIs(t, CheckVMAccess(ctx, db, vm, biller, false, AccessRead), ErrVMNotFound)
	require.Empty(t, CustomerVMScope(ctx, db, biller).Grants)

	// The account's own resources: a role that views reads them, one that
	// operates changes them, and only over every VM of the account.
	for _, c := range []struct {
		name, user, what string
		ok               bool
	}{
		{"owner changes resources", owner, AccountCanResources, true},
		{"viewer reads resources", viewer, AccountCanView, true},
		{"viewer does not change them", viewer, AccountCanResources, false},
		{"technical changes them", tech, AccountCanResources, true},
		{"scoped technical does not see them", scoped, AccountCanView, false},
		{"project admin does not change them", byProject, AccountCanResources, false},
		{"billing does not see them", biller, AccountCanView, false},
		{"billing sees billing", biller, AccountCanBilling, true},
		{"viewer does not see billing", viewer, AccountCanBilling, false},
		{"stranger gets nothing", stranger, AccountCanView, false},
	} {
		_, err := RequireAccountRole(ctx, db, c.user, owner, c.what)
		if c.ok {
			require.NoError(t, err, c.name)
		} else {
			require.Error(t, err, c.name)
		}
	}

	// A project member whose caller loaded only some columns still matches.
	partial := &models.VM{ID: other.ID, UserID: owner, Status: models.VMStatusRunning}
	require.NoError(t, CheckVMAccess(ctx, db, partial, byProject, false, AccessRead))

	// Suspension holds a member back as it holds the owner.
	suspended := *vm
	suspended.Suspended = true
	require.ErrorIs(t, CheckVMAccess(ctx, db, &suspended, tech, false, AccessChange), ErrVMSuspended)
	require.NoError(t, CheckVMAccess(ctx, db, &suspended, tech, false, AccessOperate))

	// A suspended account's members reach nothing through it.
	require.NoError(t, db.Exec(`UPDATE users SET suspended_at = ? WHERE id = ?`, time.Now(), owner).Error)
	require.ErrorIs(t, CheckVMAccess(ctx, db, vm, tech, false, AccessRead), ErrVMNotFound)
	require.NoError(t, CheckVMAccess(ctx, db, vm, stranger, true, AccessChange), "staff pass")
}

func TestCustomerVMScopeLists(t *testing.T) {
	db := teamTestDB(t)
	ctx := context.Background()
	alice := teamUser(t, db, "alice@example.com")
	bob := teamUser(t, db, "bob@example.com")
	carol := teamUser(t, db, "carol@example.com")
	project := uuid.NewString()
	require.NoError(t, db.Create(&Project{ID: project, AccountID: bob, Name: "p"}).Error)

	mine := teamVM(t, db, alice, nil)
	bobAll := teamVM(t, db, bob, nil)
	carolOne := teamVM(t, db, carol, nil)
	carolTwo := teamVM(t, db, carol, nil)
	member(t, db, bob, alice, MemberRoleReadOnly, nil, nil)
	member(t, db, carol, alice, MemberRoleTechnical, []string{carolOne.ID}, nil)

	repo := repository.NewVMRepository(db)
	scope := CustomerVMScope(ctx, db, alice)
	got, err := repo.ListFiltered(ctx, repository.VMFilter{Access: &scope}, 0, 0)
	require.NoError(t, err)
	ids := []string{}
	for _, v := range got {
		ids = append(ids, v.ID)
	}
	require.ElementsMatch(t, []string{mine.ID, bobAll.ID, carolOne.ID}, ids)
	require.NotContains(t, ids, carolTwo.ID)

	only := scope.Only(carol)
	got, err = repo.ListFiltered(ctx, repository.VMFilter{Access: &only}, 0, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, carolOne.ID, got[0].ID)

	stranger := scope.Only(uuid.NewString())
	n, err := repo.CountFiltered(ctx, repository.VMFilter{Access: &stranger})
	require.NoError(t, err)
	require.Zero(t, n, "an account the caller is not in lists nothing")
}

func TestInviteAcceptAndRevoke(t *testing.T) {
	db := teamTestDB(t)
	ctx := context.Background()
	owner := teamUser(t, db, "owner@example.com")
	existing := teamUser(t, db, "existing@example.com")
	vm := teamVM(t, db, owner, nil)

	_, err := InviteMember(ctx, db, owner, owner, MemberInput{Email: "owner@example.com", Role: MemberRoleAdmin})
	require.Error(t, err, "the account's own address")
	_, err = InviteMember(ctx, db, owner, owner, MemberInput{Email: "x@example.com", Role: "god"})
	require.Error(t, err, "an unknown role")
	_, err = InviteMember(ctx, db, owner, owner, MemberInput{Email: "x@example.com", Role: MemberRoleTechnical, VMIDs: []string{uuid.NewString()}})
	require.Error(t, err, "a VM that is not the account's")

	m, err := InviteMember(ctx, db, owner, owner, MemberInput{Email: "New@Example.com", Role: MemberRoleTechnical, VMIDs: []string{vm.ID, vm.ID}})
	require.NoError(t, err)
	require.Equal(t, "new@example.com", m.Email)
	require.Equal(t, []string{vm.ID}, m.VMIDs, "duplicates dropped")
	_, err = InviteMember(ctx, db, owner, owner, MemberInput{Email: "new@example.com", Role: MemberRoleAdmin})
	require.ErrorIs(t, err, ErrMemberExists)

	// The emailed token is the credential; the database keeps only its hash.
	token := strings.Repeat("a", 64)
	require.NoError(t, db.Model(&AccountMember{}).Where("id = ?", m.ID).Update("invite_token_hash", hashInviteToken(token)).Error)

	view, err := LookupInvitation(ctx, db, token)
	require.NoError(t, err)
	require.False(t, view.HasAccount)
	require.Equal(t, MemberRoleTechnical, view.Role)
	_, err = LookupInvitation(ctx, db, "b"+token[1:])
	require.ErrorIs(t, err, ErrInvitationInvalid)

	_, _, err = AcceptInvitation(ctx, db, AcceptInput{Token: token, Password: "short"})
	require.Error(t, err, "a weak password")
	accepted, userID, err := AcceptInvitation(ctx, db, AcceptInput{Token: token, Name: "New Person", Password: "Correct-Horse-9-Battery"})
	require.NoError(t, err)
	require.Equal(t, m.ID, accepted.ID)
	require.True(t, IsMemberOnlyAccount(ctx, db, userID), "an account made by an invitation orders nothing of its own")
	var quota models.UserQuota
	require.NoError(t, db.First(&quota, "user_id = ?", userID).Error, "a new account gets the starting quota, not none (= unlimited)")
	require.Positive(t, quota.MaxVMs)
	require.ErrorIs(t, MayOrderInAccount(ctx, db, userID, userID, ""), ErrMemberOnlyAccount)
	require.ErrorIs(t, MayOrderInAccount(ctx, db, userID, owner, ""), ErrMemberReadOnly, "limited to chosen VMs")
	require.NoError(t, CheckVMAccess(ctx, db, vm, userID, false, AccessChange))
	_, _, err = AcceptInvitation(ctx, db, AcceptInput{Token: token, Password: "Correct-Horse-9-Battery"})
	require.ErrorIs(t, err, ErrInvitationInvalid, "a token works once")

	memberships := ListMemberships(ctx, db, userID)
	require.Len(t, memberships, 1)
	require.Equal(t, owner, memberships[0].AccountID)
	require.Equal(t, []string{AccountCanOperate}, memberships[0].Can)

	// An existing account is linked as it is, and is not marked member-only.
	m2, err := InviteMember(ctx, db, owner, owner, MemberInput{Email: "existing@example.com", Role: MemberRoleAdmin})
	require.NoError(t, err)
	token2 := strings.Repeat("c", 64)
	require.NoError(t, db.Model(&AccountMember{}).Where("id = ?", m2.ID).Update("invite_token_hash", hashInviteToken(token2)).Error)
	_, linked, err := AcceptInvitation(ctx, db, AcceptInput{Token: token2})
	require.NoError(t, err)
	require.Equal(t, existing, linked)
	require.False(t, IsMemberOnlyAccount(ctx, db, existing))
	require.NoError(t, MayOrderInAccount(ctx, db, existing, owner, ""), "an admin with every VM orders into the account")
	require.NoError(t, SetMemberOnly(ctx, db, owner, true))
	require.ErrorIs(t, MayOrderInAccount(ctx, db, existing, owner, ""), ErrMemberOnlyAccount, "members order no more than a member-only account")
	require.NoError(t, SetMemberOnly(ctx, db, owner, false))
	_, err = RequireAccountRole(ctx, db, existing, owner, AccountCanManage)
	require.NoError(t, err)

	// Someone already in the account does not join it twice, under an address
	// they changed to: they are not even emailed a second invitation, and one
	// sent before the change is refused when accepted.
	require.NoError(t, db.Exec(`UPDATE users SET email = 'renamed@example.com' WHERE id = ?`, existing).Error)
	_, err = InviteMember(ctx, db, owner, owner, MemberInput{Email: "renamed@example.com", Role: MemberRoleReadOnly})
	require.ErrorIs(t, err, ErrMemberExists)
	againID := uuid.NewString()
	tokenAgain := strings.Repeat("e", 64)
	hashAgain, later := hashInviteToken(tokenAgain), time.Now().Add(time.Hour)
	require.NoError(t, db.Create(&AccountMember{ID: againID, AccountID: owner, Email: "renamed@example.com", Role: MemberRoleReadOnly,
		VMIDs: []string{}, ProjectIDs: []string{}, InviteTokenHash: &hashAgain, InviteExpiresAt: &later, CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error)
	_, _, err = AcceptInvitation(ctx, db, AcceptInput{Token: tokenAgain})
	require.ErrorIs(t, err, ErrMemberExists)
	_, err = RemoveMember(ctx, db, owner, againID)
	require.NoError(t, err)

	// A staff account has no team to invite anyone into.
	staff := teamUser(t, db, "staff@example.com")
	require.NoError(t, db.Exec(`UPDATE users SET role = 'admin' WHERE id = ?`, staff).Error)
	_, err = InviteMember(ctx, db, staff, staff, MemberInput{Email: "outsider@example.com", Role: MemberRoleAdmin})
	require.Error(t, err)
	_, err = RequireAccountRole(ctx, db, userID, owner, AccountCanManage)
	require.ErrorIs(t, err, ErrNotAccountManager)

	// Nobody changes their own membership, and an admin limited to some
	// servers does not run the team.
	adminRow := m2.ID
	_, err = UpdateMember(ctx, db, owner, existing, adminRow, MemberInput{Role: MemberRoleAdmin})
	require.ErrorIs(t, err, ErrNotAccountManager)
	_, err = UpdateMember(ctx, db, owner, owner, adminRow, MemberInput{Role: MemberRoleAdmin, VMIDs: []string{vm.ID}})
	require.NoError(t, err)
	_, err = RequireAccountRole(ctx, db, existing, owner, AccountCanManage)
	require.ErrorIs(t, err, ErrNotAccountManager, "a scoped admin")

	// An expired invitation is refused.
	m3, err := InviteMember(ctx, db, owner, owner, MemberInput{Email: "late@example.com", Role: MemberRoleReadOnly})
	require.NoError(t, err)
	token3 := strings.Repeat("d", 64)
	require.NoError(t, db.Model(&AccountMember{}).Where("id = ?", m3.ID).Updates(map[string]any{
		"invite_token_hash": hashInviteToken(token3), "invite_expires_at": time.Now().Add(-time.Minute)}).Error)
	_, _, err = AcceptInvitation(ctx, db, AcceptInput{Token: token3, Password: "Correct-Horse-9-Battery"})
	require.ErrorIs(t, err, ErrInvitationInvalid)

	// Revoking takes the access away at once.
	_, err = RemoveMember(ctx, db, owner, accepted.ID)
	require.NoError(t, err)
	require.ErrorIs(t, CheckVMAccess(ctx, db, vm, userID, false, AccessRead), ErrVMNotFound)
	_, err = RemoveMember(ctx, db, uuid.NewString(), m2.ID)
	require.ErrorIs(t, err, ErrMemberNotFound, "another account cannot remove it")
}

func TestProjects(t *testing.T) {
	db := teamTestDB(t)
	ctx := context.Background()
	owner := teamUser(t, db, "owner@example.com")
	other := teamUser(t, db, "other@example.com")
	onlyHere := teamUser(t, db, "only@example.com")
	alsoVM := teamUser(t, db, "also@example.com")

	p, err := CreateProject(ctx, db, owner, ProjectInput{Name: " Web "})
	require.NoError(t, err)
	require.Equal(t, "Web", p.Name)
	_, err = CreateProject(ctx, db, owner, ProjectInput{Name: "web"})
	require.ErrorIs(t, err, ErrProjectExists)
	_, err = CreateProject(ctx, db, other, ProjectInput{Name: "web"})
	require.NoError(t, err, "names are per account")

	vm := teamVM(t, db, owner, nil)
	foreign := teamVM(t, db, other, nil)
	require.NoError(t, SetVMProject(ctx, db, vm, p.ID))
	require.ErrorIs(t, SetVMProject(ctx, db, foreign, p.ID), ErrProjectNotFound, "a project of another account")

	list, err := ListProjects(ctx, db, owner)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.EqualValues(t, 1, list[0].VMCount)

	member(t, db, owner, onlyHere, MemberRoleTechnical, nil, []string{p.ID})
	keep := member(t, db, owner, alsoVM, MemberRoleTechnical, []string{vm.ID}, []string{p.ID})
	require.NoError(t, MayOrderInAccount(ctx, db, onlyHere, owner, p.ID))
	require.ErrorIs(t, MayOrderInAccount(ctx, db, onlyHere, owner, ""), ErrMemberReadOnly, "must order into their project")

	_, err = DeleteProject(ctx, db, owner, p.ID)
	require.NoError(t, err)
	var project *string
	require.NoError(t, db.Raw(`SELECT project_id FROM vms WHERE id = ?`, vm.ID).Scan(&project).Error)
	require.Nil(t, project, "its VMs stay, in no project")
	var n int64
	db.Model(&AccountMember{}).Where("user_id = ?", onlyHere).Count(&n)
	require.Zero(t, n, "limited to that project alone, the membership goes rather than widening to every VM")
	var kept AccountMember
	require.NoError(t, db.First(&kept, "id = ?", keep).Error)
	require.Empty(t, kept.ProjectIDs)
	require.Equal(t, []string{vm.ID}, kept.VMIDs)
}

func TestVMAccessScopeSQL(t *testing.T) {
	s := repository.VMAccessScope{UserID: "me", Grants: []repository.VMGrant{
		{AccountID: "a"},
		{AccountID: "b", VMIDs: []string{"v1"}},
		{AccountID: "c", ProjectIDs: []string{"p1"}},
	}}
	cond, args := s.SQL(func(c string) string { return "v." + c })
	require.Equal(t, "(v.user_id = ? OR v.user_id = ? OR (v.user_id = ? AND (v.id IN ?)) OR (v.user_id = ? AND (v.project_id IN ?)))", cond)
	require.Equal(t, []interface{}{"me", "a", "b", []string{"v1"}, "c", []string{"p1"}}, args)
	cond, _ = repository.VMAccessScope{UserID: "me"}.SQL(nil)
	require.Equal(t, "(user_id = ?)", cond, "no project column is named without a project grant")
}
