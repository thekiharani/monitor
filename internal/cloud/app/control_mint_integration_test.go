package app

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thekiharani/monitor/internal/cloud/access"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/control"
	"github.com/thekiharani/monitor/internal/cloud/domain"
)

func TestIntegrationControlMintTokenAndBootstrap(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	a.control = control.New(db, a.billing, a.cfg)

	ctx := context.Background()

	bootstrap, err := control.Bootstrap(ctx, db, "integration-bootstrap-op", "integration bootstrap operator")
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Token == "" || bootstrap.Kind != domain.PrincipalKindHuman || bootstrap.Assurance != domain.CredentialAssuranceMultiFactor {
		t.Fatalf("unexpected bootstrap result: %+v", bootstrap)
	}
	if len(bootstrap.Capabilities) == 0 {
		t.Fatalf("bootstrap should grant capabilities")
	}
	t.Cleanup(func() { cleanupControlPrincipal(t, db, bootstrap.PrincipalID) })

	operator, err := access.Authenticate(ctx, db, bootstrap.Token, access.AudienceControlAPI)
	if err != nil {
		t.Fatalf("bootstrap token should authenticate: %v", err)
	}
	if err := access.Authorize(ctx, db, operator, access.AccessGrant, access.PlatformScope()); err != nil {
		t.Fatalf("bootstrap operator lacks access.grant: %v", err)
	}

	actor := control.Actor{Principal: operator, UserAgent: "integration-mint"}
	minted, err := a.control.MintToken(ctx, actor, control.MintInput{Name: "integration-billing-worker", PermissionSet: "billing.operator", Reason: "integration scoped worker"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupControlPrincipal(t, db, minted.PrincipalID) })

	if minted.Token == "" || minted.Kind != domain.PrincipalKindService || minted.Assurance != domain.CredentialAssuranceWorkload {
		t.Fatalf("unexpected mint result: %+v", minted)
	}

	worker, err := access.Authenticate(ctx, db, minted.Token, access.AudienceControlAPI)
	if err != nil {
		t.Fatalf("minted token should authenticate: %v", err)
	}
	if err := access.Authorize(ctx, db, worker, access.BillingRead, access.PlatformScope()); err != nil {
		t.Fatalf("minted worker lacks billing.read: %v", err)
	}
	if err := access.Authorize(ctx, db, worker, access.AccountsSuspend, access.PlatformScope()); err == nil {
		t.Fatalf("billing.operator unexpectedly holds accounts.suspend")
	}

	var credentialID string
	if err := db.QueryRow(ctx, `SELECT id::text FROM credentials WHERE secret_hash=$1`, config.HashSecret(bootstrap.Token)).Scan(&credentialID); err != nil {
		t.Fatalf("bootstrap credential not persisted: %v", err)
	}
	if credentialID != bootstrap.CredentialID {
		t.Fatalf("persisted credential id %q != returned %q", credentialID, bootstrap.CredentialID)
	}
}

func cleanupControlPrincipal(t *testing.T, db *pgxpool.Pool, principalID string) {
	_, _ = db.Exec(context.Background(), `DELETE FROM access_grants WHERE principal_id=$1`, principalID)
	_, _ = db.Exec(context.Background(), `DELETE FROM credentials WHERE principal_id=$1`, principalID)
	_, _ = db.Exec(context.Background(), `DELETE FROM principals WHERE id=$1`, principalID)
}
