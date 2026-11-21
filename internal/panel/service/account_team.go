package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// Customer teams. A customer account can let other accounts in as members,
// each with a role and optionally limited to chosen VMs or projects. The VMs
// stay the account's: quotas, plans and billing are counted against it, and
// whatever a member makes from one of its VMs (a clone, an image) belongs to
// it too. CheckVMAccess is the one rule every VM-scoped check goes through.

// Member roles.
const (
	MemberRoleAdmin     = "admin"     // everything the owner does, members and projects included
	MemberRoleTechnical = "technical" // runs the VMs; no members, projects or billing
	MemberRoleBilling   = "billing"   // usage and billing; sees the VMs, changes nothing
	MemberRoleReadOnly  = "readonly"  // sees the VMs, changes nothing
)

// MemberRoles are the roles in the order the team page offers them.
var MemberRoles = []string{MemberRoleAdmin, MemberRoleTechnical, MemberRoleBilling, MemberRoleReadOnly}

var memberRoleWords = map[string]string{
	MemberRoleAdmin:     "Admin: everything the owner does, including the team",
	MemberRoleTechnical: "Technical: runs the servers",
	MemberRoleBilling:   "Billing: usage and billing, servers view-only",
	MemberRoleReadOnly:  "Read-only: sees the servers, changes nothing",
}

// What an account role may do beyond looking at the account's VMs.
const (
	AccountCanOperate = "operate" // change VMs and order new ones
	AccountCanManage  = "manage"  // members and projects
	AccountCanBilling = "billing" // usage and billing links
)

func roleAllows(role, what string) bool {
	switch what {
	case AccountCanOperate:
		return role == MemberRoleAdmin || role == MemberRoleTechnical
	case AccountCanManage:
		return role == MemberRoleAdmin
	case AccountCanBilling:
		return role == MemberRoleAdmin || role == MemberRoleBilling
	}
	return false
}

const (
	maxMembersPerAccount  = 50
	maxProjectsPerAccount = 100
	memberInviteTTL       = 7 * 24 * time.Hour
)

var (
	// ErrMemberReadOnly refuses a change by a member whose role only views.
	ErrMemberReadOnly = errors.New("your role in this account lets you view this VM, not change it")
	// ErrNotAccountManager refuses team or project changes by anyone but the
	// account owner and its admins.
	ErrNotAccountManager = errors.New("only the account owner or one of its admins can do this")
	ErrMemberNotFound    = errors.New("member not found")
	ErrMemberExists      = errors.New("that address is already a member or has an open invitation")
	// ErrInvitationInvalid covers a revoked, used, expired and unknown
	// invitation alike.
	ErrInvitationInvalid = errors.New("this invitation is no longer valid: it was revoked, already accepted, or has expired")
	ErrProjectNotFound   = errors.New("project not found")
	ErrProjectExists     = errors.New("a project with that name already exists")
	// ErrMemberOnlyAccount refuses an order on an account that was made by
	// accepting an invitation.
	ErrMemberOnlyAccount = errors.New("this account was made to work in a team, so it cannot order anything of its own; order in the team's account, or contact support")
)

// AccountMember is one person let into an account, or invited to it. UserID
// is empty until the invitation is accepted. VMIDs and ProjectIDs both empty
// means every VM of the account.
type AccountMember struct {
	ID              string     `json:"id" gorm:"primaryKey"`
	AccountID       string     `json:"account_id"`
	UserID          *string    `json:"user_id"`
	Email           string     `json:"email"`
	Role            string     `json:"role"`
	VMIDs           []string   `json:"vm_ids" gorm:"column:vm_ids;serializer:json"`
	ProjectIDs      []string   `json:"project_ids" gorm:"column:project_ids;serializer:json"`
	InviteTokenHash *string    `json:"-"`
	InviteExpiresAt *time.Time `json:"invite_expires_at,omitempty"`
	InvitedBy       *string    `json:"invited_by,omitempty"`
	AcceptedAt      *time.Time `json:"accepted_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	// Name is the member's name, filled for display.
	Name string `json:"name" gorm:"-"`
}

func (AccountMember) TableName() string { return "account_members" }

// allVMs reports a member who is not limited to chosen VMs or projects.
func (m *AccountMember) allVMs() bool { return len(m.VMIDs) == 0 && len(m.ProjectIDs) == 0 }

// Project groups VMs inside an account.
type Project struct {
	ID          string    `json:"id" gorm:"primaryKey"`
	AccountID   string    `json:"account_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// VMCount is filled for display.
	VMCount int64 `json:"vm_count" gorm:"-"`
}

func (Project) TableName() string { return "projects" }

// AccessNeed is what a caller wants to do with a VM.
type AccessNeed int

const (
	// AccessRead looks at the VM.
	AccessRead AccessNeed = iota
	// AccessOperate acts on it in a way a suspension does not stop: deleting
	// it, opening its console.
	AccessOperate
	// AccessChange changes it or makes something from it; refused while the
	// VM is suspended.
	AccessChange
)

// CheckVMAccess is the one rule for who may do what with a customer's VM. Staff
// pass. The owner may do anything but change a suspended VM. A member of the
// owner's account may look at the VMs their membership covers, and change
// them only with a role that operates. Anyone else gets ErrVMNotFound, the
// same answer as for a VM that does not exist.
func CheckVMAccess(ctx context.Context, db *gorm.DB, vm *models.VM, userID string, isAdmin bool, need AccessNeed) error {
	if isAdmin {
		return nil
	}
	if vm.UserID != userID {
		m := memberGrant(ctx, db, vm, userID)
		if m == nil {
			return ErrVMNotFound
		}
		if need != AccessRead && !roleAllows(m.Role, AccountCanOperate) {
			return ErrMemberReadOnly
		}
	}
	return OwnerAccess(vm, vm.UserID, false, need == AccessChange)
}

// memberGrant is the caller's membership that covers vm, or nil. A member of
// an account that is suspended or deleted reaches nothing through it.
func memberGrant(ctx context.Context, db *gorm.DB, vm *models.VM, userID string) *AccountMember {
	if db == nil || userID == "" || vm.UserID == "" {
		return nil
	}
	var rows []AccountMember
	if err := db.WithContext(ctx).Raw(`SELECT m.* FROM account_members m JOIN users u ON u.id = m.account_id
		WHERE m.account_id = ? AND m.user_id = ? AND m.accepted_at IS NOT NULL
		AND u.deleted_at IS NULL AND u.suspended_at IS NULL LIMIT 1`, vm.UserID, userID).Scan(&rows).Error; err != nil || len(rows) == 0 {
		return nil
	}
	m := &rows[0]
	if m.allVMs() || slices.Contains(m.VMIDs, vm.ID) {
		return m
	}
	if len(m.ProjectIDs) == 0 {
		return nil
	}
	project := vm.ProjectID
	if project == nil { // the caller loaded only some columns
		var p struct{ ProjectID *string }
		if db.WithContext(ctx).Table("vms").Select("project_id").Where("id = ?", vm.ID).Scan(&p).Error != nil {
			return nil
		}
		project = p.ProjectID
	}
	if project != nil && slices.Contains(m.ProjectIDs, *project) {
		return m
	}
	return nil
}

// acceptedMemberships are the accounts userID is a member of, live ones only.
func acceptedMemberships(ctx context.Context, db *gorm.DB, userID string) []AccountMember {
	var rows []AccountMember
	if db == nil || db.WithContext(ctx).Raw(`SELECT m.* FROM account_members m JOIN users u ON u.id = m.account_id
		WHERE m.user_id = ? AND m.accepted_at IS NOT NULL AND u.deleted_at IS NULL AND u.suspended_at IS NULL
		ORDER BY m.created_at`, userID).Scan(&rows).Error != nil {
		return nil
	}
	return rows
}

// CustomerVMScope is every VM a customer can see: their own and those their
// memberships cover, for a listing's WHERE clause.
func CustomerVMScope(ctx context.Context, db *gorm.DB, userID string) repository.VMAccessScope {
	scope := repository.VMAccessScope{UserID: userID}
	for _, m := range acceptedMemberships(ctx, db, userID) {
		scope.Grants = append(scope.Grants, repository.VMGrant{AccountID: m.AccountID, VMIDs: m.VMIDs, ProjectIDs: m.ProjectIDs})
	}
	return scope
}

// AccountRole is what userID is in accountID: "owner", a member role, or ""
// for nothing at all.
func AccountRole(ctx context.Context, db *gorm.DB, userID, accountID string) (string, *AccountMember) {
	if userID == accountID {
		return "owner", nil
	}
	for _, m := range acceptedMemberships(ctx, db, userID) {
		if m.AccountID == accountID {
			return m.Role, &m
		}
	}
	return "", nil
}

// RequireAccountRole checks userID may do `what` (AccountCan*) in accountID
// and returns their membership (nil for the owner). Someone with no place in
// the account gets ErrMemberNotFound, as if it did not exist.
func RequireAccountRole(ctx context.Context, db *gorm.DB, userID, accountID, what string) (*AccountMember, error) {
	role, m := AccountRole(ctx, db, userID, accountID)
	switch {
	case role == "":
		return nil, ErrMemberNotFound
	case role == "owner":
		return m, nil
	case roleAllows(role, what) && (what != AccountCanManage || m.allVMs()):
		// An admin limited to some VMs or projects runs those, not the team:
		// managing it would let them widen their own reach.
		return m, nil
	case what == AccountCanManage:
		return nil, ErrNotAccountManager
	default:
		return nil, ErrMemberReadOnly
	}
}

// IsMemberOnlyAccount reports an account made by accepting an invitation.
// A database without the column has no such accounts.
func IsMemberOnlyAccount(ctx context.Context, db *gorm.DB, userID string) bool {
	var v struct{ MemberOnly bool }
	if db == nil || db.WithContext(ctx).Table("users").Select("member_only").Where("id = ?", userID).Scan(&v).Error != nil {
		return false
	}
	return v.MemberOnly
}

// SetMemberOnly is staff letting such an account order on its own, or not.
func SetMemberOnly(ctx context.Context, db *gorm.DB, userID string, on bool) error {
	res := db.WithContext(ctx).Table("users").Where("id = ? AND deleted_at IS NULL", userID).Update("member_only", on)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// MemberInput is an invitation or a change to a member.
type MemberInput struct {
	Email      string   `json:"email"`
	Role       string   `json:"role"`
	VMIDs      []string `json:"vm_ids"`
	ProjectIDs []string `json:"project_ids"`
}

// normalize checks the role and that every chosen VM and project is the
// account's, and drops duplicates.
func (in *MemberInput) normalize(ctx context.Context, db *gorm.DB, accountID string) error {
	if !slices.Contains(MemberRoles, in.Role) {
		return fmt.Errorf("role must be one of %s", strings.Join(MemberRoles, ", "))
	}
	in.VMIDs, in.ProjectIDs = dedupe(in.VMIDs), dedupe(in.ProjectIDs)
	if len(in.VMIDs) > 500 || len(in.ProjectIDs) > maxProjectsPerAccount {
		return fmt.Errorf("too many VMs or projects chosen")
	}
	for _, c := range []struct {
		table string
		ids   []string
		what  string
	}{{"vms", in.VMIDs, "VM"}, {"projects", in.ProjectIDs, "project"}} {
		if len(c.ids) == 0 {
			continue
		}
		for _, id := range c.ids {
			if _, err := uuid.Parse(id); err != nil {
				return fmt.Errorf("%s %q is not one of this account's", c.what, id)
			}
		}
		q := db.WithContext(ctx).Table(c.table).Where("id IN ? AND "+map[string]string{"vms": "user_id", "projects": "account_id"}[c.table]+" = ?", c.ids, accountID)
		if c.table == "vms" {
			q = q.Where("deleted_at IS NULL")
		}
		var n int64
		if err := q.Count(&n).Error; err != nil {
			return err
		}
		if n != int64(len(c.ids)) {
			return fmt.Errorf("every chosen %s must be one of this account's", c.what)
		}
	}
	return nil
}

func dedupe(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func hashInviteToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ListMembers is an account's members and open invitations.
func ListMembers(ctx context.Context, db *gorm.DB, accountID string) ([]AccountMember, error) {
	var rows []AccountMember
	if err := db.WithContext(ctx).Where("account_id = ?", accountID).Order("created_at").Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := []string{}
	for _, r := range rows {
		if r.UserID != nil {
			ids = append(ids, *r.UserID)
		}
	}
	if len(ids) > 0 {
		var names []struct{ ID, Name string }
		_ = db.WithContext(ctx).Table("users").Select("id, name").Where("id IN ?", ids).Scan(&names).Error
		for i := range rows {
			for _, n := range names {
				if rows[i].UserID != nil && *rows[i].UserID == n.ID {
					rows[i].Name = n.Name
				}
			}
		}
	}
	return rows, nil
}

// InviteMember records an invitation and emails it. invitedBy is the person
// who sent it (the owner or one of the account's admins).
func InviteMember(ctx context.Context, db *gorm.DB, accountID, invitedBy string, in MemberInput) (*AccountMember, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
		return nil, fmt.Errorf("enter a valid email address")
	}
	if err := in.normalize(ctx, db, accountID); err != nil {
		return nil, err
	}
	var owner struct{ Email, Name, Role string }
	if err := db.WithContext(ctx).Table("users").Select("email, name, role").Where("id = ? AND deleted_at IS NULL", accountID).Scan(&owner).Error; err != nil {
		return nil, err
	}
	if owner.Role == string(models.RoleAdmin) {
		// The VMs a staff account owns are the provider's, not a team's.
		return nil, fmt.Errorf("a staff account has no team")
	}
	if strings.EqualFold(owner.Email, email) {
		return nil, fmt.Errorf("that is the account's own address")
	}
	var n int64
	if err := db.WithContext(ctx).Model(&AccountMember{}).Where("account_id = ?", accountID).Count(&n).Error; err != nil {
		return nil, err
	}
	if n >= maxMembersPerAccount {
		return nil, fmt.Errorf("an account can have at most %d members and invitations", maxMembersPerAccount)
	}
	if err := db.WithContext(ctx).Model(&AccountMember{}).Where("account_id = ? AND lower(email) = ?", accountID, email).Count(&n).Error; err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrMemberExists
	}
	token, err := newInviteToken()
	if err != nil {
		return nil, err
	}
	hash, expires, now := hashInviteToken(token), time.Now().UTC().Add(memberInviteTTL), time.Now().UTC()
	m := &AccountMember{ID: uuid.New().String(), AccountID: accountID, Email: email, Role: in.Role,
		VMIDs: in.VMIDs, ProjectIDs: in.ProjectIDs, InviteTokenHash: &hash, InviteExpiresAt: &expires,
		InvitedBy: &invitedBy, CreatedAt: now, UpdatedAt: now}
	if err := db.WithContext(ctx).Create(m).Error; err != nil {
		return nil, err
	}
	accountLabel := owner.Email
	if strings.TrimSpace(owner.Name) != "" {
		accountLabel = strings.TrimSpace(owner.Name) + " (" + owner.Email + ")"
	}
	if err := sendMemberInvite(ctx, db, email, token, accountLabel, in.Role, accountID); err != nil {
		// The row stays: the owner can revoke and invite again.
		return m, fmt.Errorf("the invitation was saved but could not be emailed: %w", err)
	}
	return m, nil
}

func newInviteToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func sendMemberInvite(ctx context.Context, db *gorm.DB, email, token, accountLabel, role, accountID string) error {
	brand := LoadBranding(db.WithContext(ctx))
	publicURL := PublicURL()
	link := ""
	if publicURL != "" {
		link = publicURL + "/accept-invite?token=" + token
	}
	msg := RenderEmail(templateFor(LoadEmailTemplates(db.WithContext(ctx)), EmailAccountInvite), brand, EmailData{
		Email: email, Link: link, LogoURL: EmailLogoURL(brand, publicURL),
		Extra: map[string]string{"account": accountLabel, "role": memberRoleWords[role]},
	})
	return queueEmail(ctx, db, EmailAccountInvite, email, msg, emailRefs{userID: accountID})
}

// UpdateMember changes a member's role or what they reach. Nobody changes
// their own: an admin cannot raise or widen themselves.
func UpdateMember(ctx context.Context, db *gorm.DB, accountID, actorID, memberID string, in MemberInput) (*AccountMember, error) {
	var m AccountMember
	if err := db.WithContext(ctx).Where("id = ? AND account_id = ?", memberID, accountID).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrMemberNotFound
		}
		return nil, err
	}
	if m.UserID != nil && *m.UserID == actorID {
		return nil, fmt.Errorf("%w: someone else in the account changes your own role", ErrNotAccountManager)
	}
	if err := in.normalize(ctx, db, accountID); err != nil {
		return nil, err
	}
	m.Role, m.VMIDs, m.ProjectIDs, m.UpdatedAt = in.Role, in.VMIDs, in.ProjectIDs, time.Now().UTC()
	if err := db.WithContext(ctx).Model(&AccountMember{}).Where("id = ?", m.ID).
		Updates(map[string]any{"role": m.Role, "vm_ids": jsonIDs(m.VMIDs), "project_ids": jsonIDs(m.ProjectIDs), "updated_at": m.UpdatedAt}).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// jsonIDs is a list as a JSON column value: a map update skips the
// serializer.
func jsonIDs(ids []string) string {
	raw, _ := json.Marshal(nonNil(ids))
	return string(raw)
}

// RemoveMember revokes an invitation or a membership. It returns the row as
// it was, for the audit record.
func RemoveMember(ctx context.Context, db *gorm.DB, accountID, memberID string) (*AccountMember, error) {
	var m AccountMember
	if err := db.WithContext(ctx).Where("id = ? AND account_id = ?", memberID, accountID).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrMemberNotFound
		}
		return nil, err
	}
	if err := db.WithContext(ctx).Delete(&AccountMember{}, "id = ?", m.ID).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// LeaveAccount is a member removing themselves.
func LeaveAccount(ctx context.Context, db *gorm.DB, userID, memberID string) (*AccountMember, error) {
	var m AccountMember
	if err := db.WithContext(ctx).Where("id = ? AND user_id = ?", memberID, userID).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrMemberNotFound
		}
		return nil, err
	}
	return &m, db.WithContext(ctx).Delete(&AccountMember{}, "id = ?", m.ID).Error
}

// Membership is one account the caller belongs to, as their own pages show it.
type Membership struct {
	ID           string   `json:"id"`
	AccountID    string   `json:"account_id"`
	AccountEmail string   `json:"account_email"`
	AccountName  string   `json:"account_name"`
	Role         string   `json:"role"`
	VMIDs        []string `json:"vm_ids"`
	ProjectIDs   []string `json:"project_ids"`
	// Can lists the AccountCan* the role allows.
	Can []string `json:"can"`
}

// ListMemberships are the accounts userID works in, and whether their own
// account may order anything.
func ListMemberships(ctx context.Context, db *gorm.DB, userID string) []Membership {
	out := []Membership{}
	for _, m := range acceptedMemberships(ctx, db, userID) {
		var owner struct{ Email, Name string }
		_ = db.WithContext(ctx).Table("users").Select("email, name").Where("id = ?", m.AccountID).Scan(&owner).Error
		can := []string{}
		for _, w := range []string{AccountCanOperate, AccountCanManage, AccountCanBilling} {
			if roleAllows(m.Role, w) && (w != AccountCanManage || m.allVMs()) {
				can = append(can, w)
			}
		}
		out = append(out, Membership{ID: m.ID, AccountID: m.AccountID, AccountEmail: owner.Email, AccountName: owner.Name,
			Role: m.Role, VMIDs: nonNil(m.VMIDs), ProjectIDs: nonNil(m.ProjectIDs), Can: can})
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// InvitationView is what the accept page shows before anyone signs in.
type InvitationView struct {
	Email       string `json:"email"`
	AccountName string `json:"account_name"`
	Role        string `json:"role"`
	RoleWords   string `json:"role_words"`
	// HasAccount: accepting links the existing account; otherwise one is made
	// and needs a password.
	HasAccount bool `json:"has_account"`
}

func openInvitation(ctx context.Context, db *gorm.DB, token string) (*AccountMember, error) {
	token = strings.TrimSpace(token)
	if len(token) != 64 {
		return nil, ErrInvitationInvalid
	}
	var m AccountMember
	if err := db.WithContext(ctx).Where("invite_token_hash = ? AND accepted_at IS NULL", hashInviteToken(token)).First(&m).Error; err != nil {
		return nil, ErrInvitationInvalid
	}
	if m.InviteExpiresAt == nil || time.Now().After(*m.InviteExpiresAt) {
		return nil, ErrInvitationInvalid
	}
	return &m, nil
}

// LookupInvitation describes an open invitation.
func LookupInvitation(ctx context.Context, db *gorm.DB, token string) (*InvitationView, error) {
	m, err := openInvitation(ctx, db, token)
	if err != nil {
		return nil, err
	}
	var owner struct{ Email, Name string }
	_ = db.WithContext(ctx).Table("users").Select("email, name").Where("id = ?", m.AccountID).Scan(&owner).Error
	name := owner.Name
	if strings.TrimSpace(name) == "" {
		name = owner.Email
	}
	var n int64
	db.WithContext(ctx).Model(&models.User{}).Where("lower(email) = ?", m.Email).Count(&n)
	return &InvitationView{Email: m.Email, AccountName: name, Role: m.Role, RoleWords: memberRoleWords[m.Role], HasAccount: n > 0}, nil
}

// AcceptInput is the accept page's form. Name and Password are for a person
// with no account yet.
type AcceptInput struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// AcceptInvitation turns an invitation into a membership. The token proves
// the address, so an existing account is linked as it is; otherwise an
// account is made with the password given, marked member-only so it cannot
// order anything of its own. It returns the member row and the account that
// accepted.
func AcceptInvitation(ctx context.Context, db *gorm.DB, in AcceptInput) (*AccountMember, string, error) {
	m, err := openInvitation(ctx, db, in.Token)
	if err != nil {
		return nil, "", err
	}
	var userID string
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var u models.User
		err := tx.Where("lower(email) = ?", m.Email).First(&u).Error
		switch {
		case err == nil:
			if u.Role == models.RoleAdmin {
				return fmt.Errorf("a staff account cannot join a customer's team")
			}
			if u.ID.String() == m.AccountID {
				return ErrInvitationInvalid
			}
			userID = u.ID.String()
		case errors.Is(err, gorm.ErrRecordNotFound):
			if verr := ValidatePasswordStrength(tx, in.Password); verr != nil {
				return verr
			}
			hash, herr := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
			if herr != nil {
				return herr
			}
			name := strings.TrimSpace(in.Name)
			if len(name) > 255 {
				name = name[:255]
			}
			u = models.User{Email: m.Email, Name: name, PasswordHash: string(hash), Role: models.RoleClient}
			if cerr := tx.Create(&u).Error; cerr != nil {
				// Most likely the address of an account deleted earlier, which
				// still holds it.
				return fmt.Errorf("an account cannot be made for %s here; contact support", m.Email)
			}
			userID = u.ID.String()
			if uerr := tx.Table("users").Where("id = ?", userID).Update("member_only", true).Error; uerr != nil {
				return uerr
			}
			// Without a quota row the account would order without limit the
			// day staff let it order.
			if qerr := ApplyNewClientQuota(ctx, tx, userID); qerr != nil {
				return qerr
			}
		default:
			return err
		}
		// One membership per person and account: a second (an invitation sent to
		// an address they changed to) would leave their role to chance.
		var already int64
		if cerr := tx.Model(&AccountMember{}).Where("account_id = ? AND user_id = ? AND id <> ?", m.AccountID, userID, m.ID).
			Count(&already).Error; cerr != nil {
			return cerr
		}
		if already > 0 {
			return ErrMemberExists
		}
		res := tx.Model(&AccountMember{}).Where("id = ? AND accepted_at IS NULL", m.ID).Updates(map[string]any{
			"user_id": userID, "accepted_at": time.Now().UTC(), "invite_token_hash": nil, "invite_expires_at": nil, "updated_at": time.Now().UTC(),
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrInvitationInvalid
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	m.UserID = &userID
	return m, userID, nil
}

// ListProjects is an account's projects with how many VMs each holds.
func ListProjects(ctx context.Context, db *gorm.DB, accountID string) ([]Project, error) {
	var rows []Project
	if err := db.WithContext(ctx).Where("account_id = ?", accountID).Order("lower(name)").Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		db.WithContext(ctx).Table("vms").Where("project_id = ? AND deleted_at IS NULL", rows[i].ID).Count(&rows[i].VMCount)
	}
	return rows, nil
}

// ProjectInput names a project.
type ProjectInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (in *ProjectInput) normalize() error {
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if in.Name == "" || len(in.Name) > 100 {
		return fmt.Errorf("a project needs a name of up to 100 characters")
	}
	if len(in.Description) > 500 {
		return fmt.Errorf("the description can be up to 500 characters")
	}
	return nil
}

func projectNameTaken(ctx context.Context, db *gorm.DB, accountID, name, except string) (bool, error) {
	var n int64
	q := db.WithContext(ctx).Model(&Project{}).Where("account_id = ? AND lower(name) = ?", accountID, strings.ToLower(name))
	if except != "" {
		q = q.Where("id <> ?", except)
	}
	err := q.Count(&n).Error
	return n > 0, err
}

// CreateProject adds a project to an account.
func CreateProject(ctx context.Context, db *gorm.DB, accountID string, in ProjectInput) (*Project, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	var n int64
	if err := db.WithContext(ctx).Model(&Project{}).Where("account_id = ?", accountID).Count(&n).Error; err != nil {
		return nil, err
	}
	if n >= maxProjectsPerAccount {
		return nil, fmt.Errorf("an account can have at most %d projects", maxProjectsPerAccount)
	}
	if taken, err := projectNameTaken(ctx, db, accountID, in.Name, ""); err != nil || taken {
		if err == nil {
			err = ErrProjectExists
		}
		return nil, err
	}
	now := time.Now().UTC()
	p := &Project{ID: uuid.New().String(), AccountID: accountID, Name: in.Name, Description: in.Description, CreatedAt: now, UpdatedAt: now}
	return p, db.WithContext(ctx).Create(p).Error
}

// UpdateProject renames a project.
func UpdateProject(ctx context.Context, db *gorm.DB, accountID, id string, in ProjectInput) (*Project, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	var p Project
	if err := db.WithContext(ctx).Where("id = ? AND account_id = ?", id, accountID).First(&p).Error; err != nil {
		return nil, ErrProjectNotFound
	}
	if taken, err := projectNameTaken(ctx, db, accountID, in.Name, p.ID); err != nil || taken {
		if err == nil {
			err = ErrProjectExists
		}
		return nil, err
	}
	p.Name, p.Description, p.UpdatedAt = in.Name, in.Description, time.Now().UTC()
	return &p, db.WithContext(ctx).Model(&Project{}).Where("id = ?", p.ID).
		Updates(map[string]any{"name": p.Name, "description": p.Description, "updated_at": p.UpdatedAt}).Error
}

// DeleteProject removes a project. Its VMs stay, in no project, and members
// limited to it lose it from their list.
func DeleteProject(ctx context.Context, db *gorm.DB, accountID, id string) (*Project, error) {
	var p Project
	if err := db.WithContext(ctx).Where("id = ? AND account_id = ?", id, accountID).First(&p).Error; err != nil {
		return nil, ErrProjectNotFound
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("vms").Where("project_id = ?", p.ID).Update("project_id", nil).Error; err != nil {
			return err
		}
		var members []AccountMember
		if err := tx.Where("account_id = ?", accountID).Find(&members).Error; err != nil {
			return err
		}
		for _, m := range members {
			if !slices.Contains(m.ProjectIDs, p.ID) {
				continue
			}
			rest := slices.DeleteFunc(slices.Clone(m.ProjectIDs), func(x string) bool { return x == p.ID })
			if len(rest) == 0 && len(m.VMIDs) == 0 {
				// Limited to nothing but this project: emptying the list would
				// widen them to every VM, so the membership goes instead.
				if err := tx.Delete(&AccountMember{}, "id = ?", m.ID).Error; err != nil {
					return err
				}
				continue
			}
			if err := tx.Model(&AccountMember{}).Where("id = ?", m.ID).Update("project_ids", jsonIDs(rest)).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&Project{}, "id = ?", p.ID).Error
	})
	return &p, err
}

// SetVMProject puts a VM in one of its account's projects, or in none ("").
func SetVMProject(ctx context.Context, db *gorm.DB, vm *models.VM, projectID string) error {
	var value any
	if projectID != "" {
		if err := ProjectBelongs(ctx, db, projectID, vm.UserID); err != nil {
			return err
		}
		value = projectID
	}
	return db.WithContext(ctx).Table("vms").Where("id = ?", vm.ID).Update("project_id", value).Error
}

// ProjectBelongs checks a project is accountID's.
func ProjectBelongs(ctx context.Context, db *gorm.DB, projectID, accountID string) error {
	if _, err := uuid.Parse(projectID); err != nil {
		return ErrProjectNotFound
	}
	var n int64
	if err := db.WithContext(ctx).Model(&Project{}).Where("id = ? AND account_id = ?", projectID, accountID).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// MayOrderInAccount checks userID may order a new VM into accountID (their
// own, or one they are a member of with a role that operates), and into
// projectID when one is given. A member limited to chosen VMs cannot order:
// the new VM would be out of their reach the moment it exists.
func MayOrderInAccount(ctx context.Context, db *gorm.DB, userID, accountID, projectID string) error {
	if userID == accountID {
		if IsMemberOnlyAccount(ctx, db, userID) {
			return ErrMemberOnlyAccount
		}
		if projectID != "" {
			return ProjectBelongs(ctx, db, projectID, accountID)
		}
		return nil
	}
	m, err := RequireAccountRole(ctx, db, userID, accountID, AccountCanOperate)
	if err != nil {
		return err
	}
	if IsMemberOnlyAccount(ctx, db, accountID) {
		// Its members order no more than it does.
		return ErrMemberOnlyAccount
	}
	if projectID != "" {
		if err := ProjectBelongs(ctx, db, projectID, accountID); err != nil {
			return err
		}
	}
	switch {
	case m.allVMs():
		return nil
	case len(m.ProjectIDs) > 0 && projectID != "" && slices.Contains(m.ProjectIDs, projectID):
		return nil
	case len(m.ProjectIDs) > 0:
		return fmt.Errorf("%w: choose one of the projects you work in", ErrMemberReadOnly)
	default:
		return fmt.Errorf("%w: you work on chosen servers only, so you cannot order new ones here", ErrMemberReadOnly)
	}
}

// DB is the service's database, for the team rules handlers apply.
func (s *VMService) DB() *gorm.DB { return s.db }

// clearForeignProject takes a VM out of a project that is not its owner's,
// after it has been given to another account.
func clearForeignProject(ctx context.Context, tx *gorm.DB, vmID, owner string) error {
	if !tx.Migrator().HasColumn(&models.VM{}, "project_id") {
		return nil // a test schema from before projects
	}
	return tx.WithContext(ctx).Exec(`UPDATE vms SET project_id = NULL WHERE id = ? AND project_id IS NOT NULL
		AND project_id NOT IN (SELECT id FROM projects WHERE account_id = ?)`, vmID, owner).Error
}
