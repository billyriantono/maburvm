package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// The node installer is for new machines. The Add node flow creates a
// short-lived, single-use install token; the installer on the new machine
// trades it for the node's agent credential, which no screen ever showed.

var (
	// ErrNodeHasAgent refuses an install token for a node an agent already
	// answers for (or that holds VMs): the installer is never for those.
	ErrNodeHasAgent = errors.New("this node already runs an agent, and the installer is only for new machines. Update its agent from Settings, System")
	// ErrEnrollmentInvalid is a wrong, expired or used install token. One
	// message for all three, so a guess learns nothing.
	ErrEnrollmentInvalid = errors.New("the install token is wrong, expired or already used. Create a new install command on the node's page")
)

// NodeEnrollmentTTL is how long an install token is good for.
const NodeEnrollmentTTL = time.Hour

const enrollmentTokenPrefix = "mbe_"

// NodeEnrollment is a fresh install token; Token is shown once and not stored.
type NodeEnrollment struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// EnrolledNode is what the installer gets for a good token.
type EnrolledNode struct {
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name"`
	AgentToken string `json:"agent_token"`
}

// PendingEnrollment describes a good token without using it.
type PendingEnrollment struct {
	NodeID    string    `json:"node_id"`
	NodeName  string    `json:"node_name"`
	ExpiresAt time.Time `json:"expires_at"`
}

func hashEnrollmentToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// NodeAcceptsInstaller reports whether a node may get an install token: it
// exists, no agent has answered for it and no VM lives on it.
func NodeAcceptsInstaller(ctx context.Context, db *gorm.DB, nodeID string) (bool, error) {
	var row struct {
		CertFingerprint string
		LastReportAt    *time.Time
	}
	res := db.WithContext(ctx).Raw(`SELECT cert_fingerprint, last_report_at FROM nodes WHERE id = ? AND deleted_at IS NULL`, nodeID).Scan(&row)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, ErrNodeNotFound
	}
	if row.CertFingerprint != "" || row.LastReportAt != nil {
		return false, nil
	}
	var vms int64
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM vms WHERE node_id = ? AND deleted_at IS NULL`, nodeID).Scan(&vms).Error; err != nil {
		return false, err
	}
	return vms == 0, nil
}

// IssueNodeEnrollment creates an install token for a new node. Tokens issued
// before for the same node stop working.
func IssueNodeEnrollment(ctx context.Context, db *gorm.DB, nodeID, createdBy string) (*NodeEnrollment, error) {
	ok, err := NodeAcceptsInstaller(ctx, db, nodeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNodeHasAgent
	}
	secret, err := randomHex(24)
	if err != nil {
		return nil, ErrTokenGenerationFailed
	}
	token := enrollmentTokenPrefix + secret
	now := time.Now().UTC()
	out := &NodeEnrollment{Token: token, ExpiresAt: now.Add(NodeEnrollmentTTL)}
	var by *string
	if _, err := uuid.Parse(createdBy); err == nil {
		by = &createdBy
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM node_enrollments WHERE node_id = ? AND used_at IS NULL`, nodeID).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO node_enrollments (id, node_id, token_hash, created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), nodeID, hashEnrollmentToken(token), by, now, out.ExpiresAt).Error
	})
	if err != nil {
		return nil, fmt.Errorf("could not store the install token: %w", err)
	}
	return out, nil
}

// CheckNodeEnrollment describes a good token without using it, so the
// installer can stop before installing anything when the token is bad.
func CheckNodeEnrollment(ctx context.Context, db *gorm.DB, token string) (*PendingEnrollment, error) {
	if !strings.HasPrefix(strings.TrimSpace(token), enrollmentTokenPrefix) {
		return nil, ErrEnrollmentInvalid
	}
	var row struct {
		NodeID    string
		Name      string
		ExpiresAt time.Time
	}
	res := db.WithContext(ctx).Raw(`SELECT e.node_id, n.name, e.expires_at FROM node_enrollments e JOIN nodes n ON n.id = e.node_id
		WHERE e.token_hash = ? AND e.used_at IS NULL AND e.expires_at > ? AND n.deleted_at IS NULL`,
		hashEnrollmentToken(token), time.Now().UTC()).Scan(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrEnrollmentInvalid
	}
	// The redeem would refuse it; say so before the installer touches the host.
	ok, err := NodeAcceptsInstaller(ctx, db, row.NodeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNodeHasAgent
	}
	return &PendingEnrollment{NodeID: row.NodeID, NodeName: row.Name, ExpiresAt: row.ExpiresAt}, nil
}

// RedeemNodeEnrollment uses a token up and hands out the node's agent
// credential, freshly generated. A node an agent already answers for is
// refused even with a good token, so a leaked token cannot take over a node.
func RedeemNodeEnrollment(ctx context.Context, db *gorm.DB, token, from string) (*EnrolledNode, error) {
	if !strings.HasPrefix(strings.TrimSpace(token), enrollmentTokenPrefix) {
		return nil, ErrEnrollmentInvalid
	}
	agentToken, err := randomHex(TokenLength)
	if err != nil {
		return nil, ErrTokenGenerationFailed
	}
	sealed, err := models.SealSecret(agentToken)
	if err != nil {
		return nil, err
	}
	hash := hashEnrollmentToken(token)
	var out *EnrolledNode
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		// The guard in the WHERE makes it single-use under concurrency: a
		// second redeem waits for the first and then matches nothing.
		res := tx.Exec(`UPDATE node_enrollments SET used_at = ?, used_from = ? WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
			now, truncateString(from, 64), hash, now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrEnrollmentInvalid
		}
		var nodeID string
		if err := tx.Raw(`SELECT node_id FROM node_enrollments WHERE token_hash = ?`, hash).Scan(&nodeID).Error; err != nil {
			return err
		}
		ok, err := NodeAcceptsInstaller(ctx, tx, nodeID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNodeHasAgent
		}
		var name string
		if err := tx.Raw(`SELECT name FROM nodes WHERE id = ?`, nodeID).Scan(&name).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Node{}).Where("id = ?", nodeID).Update("token", sealed).Error; err != nil {
			return err
		}
		out = &EnrolledNode{NodeID: nodeID, NodeName: name, AgentToken: agentToken}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func truncateString(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
