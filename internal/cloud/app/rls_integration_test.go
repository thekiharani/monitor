package app

import (
	"context"
	"fmt"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func seedRLSAccount(t *testing.T, a *App, label string) (accountID, code string) {
	t.Helper()
	stamp := time.Now().UnixNano()
	address := fmt.Sprintf("rls-%s-%d@example.test", label, stamp)
	accountID = createTestAccount(t, a.db, address)
	t.Cleanup(func() { deleteTestAccount(t, a.db, address) })
	code = fmt.Sprintf("rls-invitee-%s-%d@example.test", label, stamp)
	if _, err := a.db.Exec(context.Background(),
		`INSERT INTO account_invites(account_id,email,role,token_hash,inviter_id,expires_at)
		 VALUES($1,$2,'member',$3,$4,now()+interval '1 day')`,
		accountID, code, config.HashSecret(code), principalOf(t, a.db, address)); err != nil {
		t.Fatalf("seed invite: %v", err)
	}
	return accountID, code
}

func TestIntegrationRowLevelSecurityConfinesUnfilteredReads(t *testing.T) {
	a := newCoreIntegrationApp(t, openIntegrationDB(t), &capturingMail{})
	firstAccount, firstCode := seedRLSAccount(t, a, "a")
	secondAccount, secondCode := seedRLSAccount(t, a, "b")

	visible := func(accountID string) []string {
		t.Helper()
		var codes []string
		if err := a.readTenant(context.Background(), accountID, func(tx pgx.Tx) error {
			rows, err := tx.Query(context.Background(), `SELECT email FROM account_invites`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var code string
				if err = rows.Scan(&code); err != nil {
					return err
				}
				codes = append(codes, code)
			}
			return rows.Err()
		}); err != nil {
			t.Fatalf("unfiltered read as %s: %v", accountID, err)
		}
		return codes
	}

	for _, tc := range []struct {
		accountID, own, foreign string
	}{
		{firstAccount, firstCode, secondCode},
		{secondAccount, secondCode, firstCode},
	} {
		codes := visible(tc.accountID)
		var sawOwn bool
		for _, code := range codes {
			if code == tc.foreign {
				t.Errorf("an unfiltered SELECT as %s returned another tenant's invite %q", tc.accountID, tc.foreign)
			}
			if code == tc.own {
				sawOwn = true
			}
		}
		if !sawOwn {
			t.Errorf("an unfiltered SELECT as %s did not return its own invite %q", tc.accountID, tc.own)
		}
	}
}

func TestIntegrationRowLevelSecurityFailsClosedWithoutATenant(t *testing.T) {
	a := newCoreIntegrationApp(t, openIntegrationDB(t), &capturingMail{})
	seedRLSAccount(t, a, "closed")

	ctx := context.Background()
	tx, err := a.tenant.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM account_invites`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("a connection with no tenant set saw %d invites, want 0", count)
	}
	if err = a.withTenant(ctx, "", func(pgx.Tx) error { return nil }); err == nil {
		t.Error("withTenant with an empty account = nil, want a refusal")
	}
}

func TestIntegrationRowLevelSecurityRejectsCrossTenantWrites(t *testing.T) {
	a := newCoreIntegrationApp(t, openIntegrationDB(t), &capturingMail{})
	attacker, _ := seedRLSAccount(t, a, "att")
	victim, victimCode := seedRLSAccount(t, a, "vic")

	ctx := context.Background()
	err := a.withTenant(ctx, attacker, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO account_invites(account_id,email,role,token_hash,inviter_id,expires_at)
			 SELECT $1,$2,'admin',$3,principal_id,now()+interval '1 day' FROM account_memberships WHERE account_id=$4 LIMIT 1`,
			victim, fmt.Sprintf("planted-%d@example.test", time.Now().UnixNano()), config.HashSecret(fmt.Sprint(time.Now().UnixNano())), attacker)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Errorf("inserting an invite into another tenant = %v, want a row-level security violation", err)
	}

	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"update", `UPDATE account_invites SET role='admin' WHERE email=$1`},
		{"delete", `DELETE FROM account_invites WHERE email=$1`},
	} {
		var affected int64
		if err = a.withTenant(ctx, attacker, func(tx pgx.Tx) error {
			tag, execErr := tx.Exec(ctx, tc.sql, victimCode)
			affected = tag.RowsAffected()
			return execErr
		}); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if affected != 0 {
			t.Errorf("a cross-tenant %s changed %d rows, want 0", tc.name, affected)
		}
	}

	var role string
	if err = a.db.QueryRow(ctx, `SELECT role FROM account_invites WHERE email=$1`, victimCode).Scan(&role); err != nil {
		t.Fatalf("victim invite: %v", err)
	}
	if role != "member" {
		t.Errorf("the victim's invite now grants %q", role)
	}
}

func TestIntegrationTenantRoleCannotReachControlPlaneTables(t *testing.T) {
	a := newCoreIntegrationApp(t, openIntegrationDB(t), &capturingMail{})
	accountID, _ := seedRLSAccount(t, a, "ctl")

	ctx := context.Background()
	for _, table := range []string{"audit_events", "access_grants", "auth_challenges", "rate_buckets", "plan_catalog"} {
		err := a.readTenant(ctx, accountID, func(tx pgx.Tx) error {
			var count int
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count)
		})
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("reading %s as the tenant role = %v, want permission denied", table, err)
		}
	}
}
