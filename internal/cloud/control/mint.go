package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"uuid"

	"github.com/thekiharani/monitor/internal/cloud/access"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/dbx"
	"github.com/thekiharani/monitor/internal/cloud/domain"
)

const controlTokenLifetime = 90 * 24 * time.Hour

const controlTokenMarker = "nmon_ctl_"

type MintInput struct {
	Name          string
	PermissionSet string
	Reason        string
}

type MintResult struct {
	Token        string    `json:"token"`
	CredentialID string    `json:"credential_id"`
	PrincipalID  string    `json:"principal_id"`
	Kind         string    `json:"kind"`
	Assurance    string    `json:"assurance"`
	ExpiresAt    time.Time `json:"expires_at"`
	Capabilities []string  `json:"capabilities"`
}

func (s *Service) MintToken(ctx context.Context, actor Actor, input MintInput) (MintResult, error) {
	name, err := validateMintName(input.Name)
	if err != nil {
		return MintResult{}, err
	}
	capabilities, ok := access.Capabilities(input.PermissionSet)
	if !ok {
		return MintResult{}, fmt.Errorf("%w: unknown permission set", ErrInvalid)
	}
	reason, err := validateMintReason(input.Reason)
	if err != nil {
		return MintResult{}, err
	}
	raw, err := newControlToken()
	if err != nil {
		return MintResult{}, err
	}
	result := MintResult{
		Token:        raw,
		CredentialID: uuid.NewV7().String(),
		PrincipalID:  uuid.NewV7().String(),
		Kind:         domain.PrincipalKindService,
		Assurance:    domain.CredentialAssuranceWorkload,
		ExpiresAt:    time.Now().UTC().Add(controlTokenLifetime),
		Capabilities: capabilities,
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return MintResult{}, err
	}
	defer dbx.Rollback(ctx, tx)
	if err = access.Authorize(ctx, tx, actor.Principal, access.AccessGrant, access.PlatformScope()); err != nil {
		return MintResult{}, err
	}
	if err = insertMintedCredential(ctx, tx, result, name, raw, capabilities, actor.Principal.ID, reason); err != nil {
		return MintResult{}, err
	}
	if err = writeMintAudit(ctx, tx, actor.Principal.ID, actor.Principal.CredentialID, result, name, input.PermissionSet, reason, actor.RequestID, actor.SourceIP, actor.UserAgent); err != nil {
		return MintResult{}, err
	}
	return result, tx.Commit(ctx)
}

func Bootstrap(ctx context.Context, db *pgxpool.Pool, name, reason string) (MintResult, error) {
	name, err := validateMintName(name)
	if err != nil {
		return MintResult{}, err
	}
	reason, err = validateMintReason(reason)
	if err != nil {
		return MintResult{}, err
	}
	capabilities, _ := access.Capabilities("platform.owner")
	raw, err := newControlToken()
	if err != nil {
		return MintResult{}, err
	}
	result := MintResult{
		Token:        raw,
		CredentialID: uuid.NewV7().String(),
		PrincipalID:  uuid.NewV7().String(),
		Kind:         domain.PrincipalKindHuman,
		Assurance:    domain.CredentialAssuranceMultiFactor,
		ExpiresAt:    time.Now().UTC().Add(controlTokenLifetime),
		Capabilities: capabilities,
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return MintResult{}, err
	}
	defer dbx.Rollback(ctx, tx)
	if err = insertMintedCredential(ctx, tx, result, name, raw, capabilities, result.PrincipalID, reason); err != nil {
		return MintResult{}, err
	}
	if err = writeMintAudit(ctx, tx, result.PrincipalID, result.CredentialID, result, name, "platform.owner", reason, "", "", ""); err != nil {
		return MintResult{}, err
	}
	return result, tx.Commit(ctx)
}

func validateMintName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return "", fmt.Errorf("%w: name must be between 1 and 64 characters", ErrInvalid)
	}
	return name, nil
}

func validateMintReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) < 3 || len(reason) > 1000 {
		return "", fmt.Errorf("%w: reason must be between 3 and 1000 characters", ErrInvalid)
	}
	return reason, nil
}

func newControlToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate control token: %w", err)
	}
	return controlTokenMarker + hex.EncodeToString(bytes), nil
}

func controlKeyPrefix(raw string) string {
	body := strings.TrimPrefix(raw, controlTokenMarker)
	return controlTokenMarker + body[:min(4, len(body))]
}

func insertMintedCredential(ctx context.Context, tx pgx.Tx, result MintResult, name, raw string, capabilities []string, grantorID, reason string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO principals(id,kind,display_name) VALUES($1,$2,$3)`, result.PrincipalID, result.Kind, name); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO credentials(id,principal_id,kind,audience,name,key_prefix,secret_hash,assurance,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		result.CredentialID, result.PrincipalID, domain.CredentialKindAPIKey, domain.CredentialAudienceControlAPI, name, controlKeyPrefix(raw), config.HashSecret(raw), result.Assurance, result.ExpiresAt); err != nil {
		return err
	}
	for _, capability := range capabilities {
		if _, err := tx.Exec(ctx, `INSERT INTO access_grants(principal_id,capability,scope_type,grantor_id,reason) VALUES($1,$2,$3,$4,$5)`, result.PrincipalID, capability, domain.ScopeTypePlatform, grantorID, reason); err != nil {
			return err
		}
	}
	return nil
}

func writeMintAudit(ctx context.Context, tx pgx.Tx, principalID, credentialID string, result MintResult, name, permissionSet, reason, requestID, sourceIP, userAgent string) error {
	after, err := json.Marshal(map[string]any{
		"principal_id":   result.PrincipalID,
		"credential_id":  result.CredentialID,
		"kind":           result.Kind,
		"assurance":      result.Assurance,
		"name":           name,
		"permission_set": permissionSet,
		"capabilities":   result.Capabilities,
	})
	if err != nil {
		return fmt.Errorf("marshal mint audit: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_events(principal_id,credential_id,capability,action,target_type,target_id,reason,before_state,after_state,request_id,source_ip,user_agent)
		VALUES($1,$2,$3,$4,$5,$6,$7,NULL,$8,NULLIF($9,''),NULLIF($10,'')::inet,NULLIF($11,''))`,
		principalID, credentialID, access.AccessGrant, "credential.minted", "principal", result.PrincipalID, reason, after, requestID, sourceIP, userAgent)
	return err
}
