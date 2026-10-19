package app

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"sync"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/dbx"
	"github.com/thekiharani/monitor/internal/cloud/email"
)

func (a *App) cleanupWorker(ctx context.Context) {
	hourly := time.NewTicker(time.Hour)
	daily := time.NewTicker(24 * time.Hour)
	defer hourly.Stop()
	defer daily.Stop()
	for {
		select {
		case <-hourly.C:
			a.cleanupHourly(ctx)
		case <-daily.C:
			a.cleanupDaily(ctx)
			a.maintainResults(ctx)
			a.purgeClosedAccounts(ctx)
		case <-a.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

const (
	cleanupTimeout = 2 * time.Minute
	sampleTimeout  = 5 * time.Second
	emailTimeout   = 30 * time.Second
	billingTimeout = 5 * time.Minute
)

func (a *App) cleanupHourly(ctx context.Context) {
	a.runCleanup(ctx, "monitor:cleanup-hourly", []string{
		`DELETE FROM auth_challenges WHERE created_at<now()-interval '24 hours'`,
		`DELETE FROM rate_buckets WHERE refilled_at<now()-interval '1 hour'`,
	})
}

func (a *App) cleanupDaily(ctx context.Context) {
	a.runCleanup(ctx, "monitor:cleanup-daily", []string{
		`DELETE FROM credentials c
		 WHERE (c.expires_at<now()-interval '30 days' OR c.revoked_at<now()-interval '30 days')
		   AND NOT EXISTS (SELECT 1 FROM audit_events e WHERE e.credential_id=c.id)`,
		`DELETE FROM billing_events WHERE received_at<now()-interval '90 days' AND status IN ('processed','ignored')`,
		`DELETE FROM email_jobs WHERE (sent_at IS NOT NULL AND sent_at<now()-interval '30 days') OR (sent_at IS NULL AND attempts>=8 AND created_at<now()-interval '90 days')`,
	})
}

func (a *App) runCleanup(parent context.Context, lockKey string, statements []string) {
	ctx, cancel := context.WithTimeout(parent, cleanupTimeout)
	defer cancel()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		a.logError("cleanup begin failed", "cleanup_worker", "begin_cleanup_transaction", err, "job", lockKey, "statement_count", len(statements))
		return
	}
	defer dbx.Rollback(ctx, tx)
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, lockKey).Scan(&locked); err != nil || !locked {
		if err != nil {
			a.logError("cleanup lock failed", "cleanup_worker", "acquire_cleanup_lock", err, "job", lockKey)
		} else {
			a.logWarn("cleanup skipped because another worker holds the lock", "cleanup_worker", "acquire_cleanup_lock", nil, "job", lockKey, "skipped", true)
		}
		return
	}
	for statementIndex, statement := range statements {
		if _, err = tx.Exec(ctx, statement); err != nil {
			a.logError("cleanup statement failed", "cleanup_worker", "execute_cleanup_statement", err, "job", lockKey, "statement_index", statementIndex)
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		a.logError("cleanup commit failed", "cleanup_worker", "commit_cleanup_transaction", err, "job", lockKey, "statement_count", len(statements))
	}
}

func (a *App) emailWorker(ctx context.Context) {
	ticker := time.NewTicker(emailPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.emailWake:
			a.sendEmailBatch(ctx, emailBatchSize)
		case <-ticker.C:
			a.sendEmailBatch(ctx, emailBatchSize)
		case <-a.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

const (
	emailPollInterval = 15 * time.Second
	emailBatchSize    = 10
)

func (a *App) notifyEmail() {
	select {
	case a.emailWake <- struct{}{}:
	default:
	}
}

const emailLeaseDuration = 10 * time.Minute

type emailJob struct {
	id           string
	to           string
	templateName string
	attempts     int
	payload      []byte
}

func (a *App) sendEmailBatch(parent context.Context, batchSize int) {
	if batchSize <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(parent, emailTimeout)
	defer cancel()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		a.logError("email job claim transaction failed", "email_worker", "begin_claim_transaction", err, "batch_size", batchSize)
		return
	}
	defer dbx.Rollback(ctx, tx)
	rows, err := tx.Query(ctx, `SELECT id::text,to_email,template,attempts,payload FROM email_jobs WHERE sent_at IS NULL AND next_attempt_at<=now() AND attempts<8 ORDER BY next_attempt_at,created_at LIMIT $1 FOR UPDATE SKIP LOCKED`, batchSize)
	if err != nil {
		a.logError("email job claim failed", "email_worker", "claim_jobs", err, "batch_size", batchSize)
		return
	}
	jobs := make([]emailJob, 0, batchSize)
	for rows.Next() {
		var job emailJob
		if err = rows.Scan(&job.id, &job.to, &job.templateName, &job.attempts, &job.payload); err != nil {
			rows.Close()
			a.logError("email job scan failed", "email_worker", "scan_claimed_job", err, "batch_size", batchSize, "jobs_scanned", len(jobs))
			return
		}
		jobs = append(jobs, job)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		a.logError("email job rows failed", "email_worker", "iterate_claimed_jobs", err, "batch_size", batchSize, "jobs_scanned", len(jobs))
		return
	}
	rows.Close()
	if len(jobs) == 0 {
		return
	}
	for _, job := range jobs {
		if _, err = tx.Exec(ctx, `UPDATE email_jobs SET attempts=attempts+1,next_attempt_at=now()+$1::interval WHERE id=$2`, emailLeaseDuration.String(), job.id); err != nil {
			a.logError("email job lease update failed", "email_worker", "lease_claimed_job", err, "job_id", job.id, "attempts", job.attempts, "batch_size", len(jobs))
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		a.logError("email job claim commit failed", "email_worker", "commit_claimed_jobs", err, "batch_size", len(jobs))
		return
	}

	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Go(func() { a.sendClaimedEmail(ctx, job) })
	}
	wg.Wait()
}

func (a *App) sendClaimedEmail(ctx context.Context, job emailJob) {
	var payloadData any
	if len(job.payload) > 0 {
		if err := json.Unmarshal(job.payload, &payloadData); err != nil {
			a.metrics.sesFailures.Add(1)
			a.logError("email job payload invalid", "email_worker", "decode_job_payload", err, "job_id", job.id, "template", job.templateName, "attempts", job.attempts)
			a.failEmailJob(ctx, job.id, job.attempts, "invalid email job payload")
			return
		}
	}
	message, renderErr := email.Render(job.templateName, payloadData)
	if renderErr != nil {
		a.metrics.sesFailures.Add(1)
		a.logError("email template render failed", "email_worker", "render_email", renderErr, "job_id", job.id, "template", job.templateName, "attempts", job.attempts)
		a.failEmailJob(ctx, job.id, job.attempts, renderErr.Error())
		return
	}
	message.To = job.to
	sendErr := a.mail.Send(ctx, message)
	recordCtx, cancelRecord := dbx.Cleanup(ctx)
	defer cancelRecord()
	if sendErr == nil {
		if _, err := a.db.Exec(recordCtx, `UPDATE email_jobs SET sent_at=now(),last_error=NULL WHERE id=$1`, job.id); err != nil {
			a.logError("email job completion update failed", "email_worker", "mark_email_sent", err, "job_id", job.id, "template", job.templateName, "attempts", job.attempts)
		}
		return
	}
	a.metrics.sesFailures.Add(1)
	a.logError("email delivery failed", "email_worker", "send_email", sendErr, "job_id", job.id, "template", job.templateName, "attempts", job.attempts)
	a.failEmailJob(recordCtx, job.id, job.attempts, "email delivery failed")
}

func (a *App) failEmailJob(ctx context.Context, id string, attempts int, message string) {
	backoff := time.Duration(1<<min(attempts, 7)) * time.Minute
	if _, err := a.db.Exec(ctx, `UPDATE email_jobs SET next_attempt_at=now()+$1::interval,last_error=$2 WHERE id=$3`, backoff.String(), message, id); err != nil {
		a.logError("email job failure update failed", "email_worker", "record_email_failure", err, "job_id", id, "attempts", attempts, "backoff", backoff.String(), "failure_reason", message)
	}
}

func (a *App) withJobLock(ctx context.Context, key, component, operation string, run func(context.Context)) {
	conn, err := a.db.Acquire(ctx)
	if err != nil {
		a.logError("job lock connection acquire failed", component, operation, err, "job", key)
		return
	}
	defer conn.Release()
	locked, err := acquireAdvisoryLock(ctx, conn, key)
	if err != nil {
		a.logError("job lock failed", component, operation, err, "job", key)
		return
	}
	if !locked {
		a.logWarn("job skipped because another instance holds the lock", component, operation, nil, "job", key, "skipped", true)
		return
	}
	defer func() {
		if releaseErr := releaseAdvisoryLock(ctx, conn, key); releaseErr != nil {
			a.logError("job lock release failed", component, operation, releaseErr, "job", key)
		}
	}()
	run(ctx)
}

const (
	closedAccountRetention = 30 * 24 * time.Hour
	purgeBatch             = 20
)

func (a *App) purgeClosedAccounts(parent context.Context) {
	a.withJobLock(parent, "monitor:account-purge", "purge_worker", "acquire_purge_lock", a.purgeClosedAccountsLocked)
}

func (a *App) purgeClosedAccountsLocked(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, cleanupTimeout)
	defer cancel()

	rows, err := a.db.Query(ctx,
		`SELECT id::text FROM accounts
		 WHERE status='closed' AND purged_at IS NULL AND deleted_at IS NOT NULL
		   AND deleted_at<now()-$1::interval
		 ORDER BY deleted_at LIMIT $2`,
		closedAccountRetention.String(), purgeBatch,
	)
	if err != nil {
		a.logError("purge query failed", "purge_worker", "select_closed_accounts", err)
		return
	}
	accounts := make([]string, 0, purgeBatch)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			a.logError("purge scan failed", "purge_worker", "scan_closed_account", err)
			return
		}
		accounts = append(accounts, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		a.logError("purge iteration failed", "purge_worker", "iterate_closed_accounts", err)
		return
	}

	for _, account := range accounts {
		if err = a.purgeAccount(ctx, account); err != nil {
			a.logError("purge failed", "purge_worker", "purge_account", err, "account_id", account)
			continue
		}
		a.logEvent("closed account erased", "purge_worker", "purge_account",
			"account_id", account, "retention", closedAccountRetention.String())
	}
}

var accountPurgeStatements = []string{
	`DELETE FROM status_post_updates WHERE account_id=$1`,
	`DELETE FROM status_posts WHERE account_id=$1`,
	`DELETE FROM status_page_items WHERE account_id=$1`,
	`DELETE FROM status_pages WHERE account_id=$1`,
	`DELETE FROM notifications WHERE account_id=$1`,
	`DELETE FROM monitor_channels WHERE account_id=$1`,
	`DELETE FROM alert_channels WHERE account_id=$1`,
	`DELETE FROM maintenance_windows WHERE account_id=$1`,
	`DELETE FROM incidents WHERE account_id=$1`,
	`DELETE FROM check_results WHERE account_id=$1`,
	`DELETE FROM monitor_daily WHERE account_id=$1`,
	`DELETE FROM monitor_schedules WHERE account_id=$1`,
	`DELETE FROM monitors WHERE account_id=$1`,
	`DELETE FROM account_invites WHERE account_id=$1`,
	`DELETE FROM account_monthly_usage WHERE account_id=$1`,
	`DELETE FROM billing_events WHERE account_id=$1`,
	`DELETE FROM billing_checkouts WHERE account_id=$1`,
	`DELETE FROM billing_subscriptions WHERE account_id=$1`,
	`DELETE FROM email_jobs WHERE account_id=$1`,
	`DELETE FROM credentials WHERE account_id=$1
	   AND id NOT IN (SELECT credential_id FROM audit_events WHERE credential_id IS NOT NULL)`,
	`DELETE FROM account_memberships WHERE account_id=$1`,
	`UPDATE accounts SET purged_at=now(),updated_at=now() WHERE id=$1`,
}

var orphanPurgeStatements = []string{
	`DELETE FROM access_grants WHERE principal_id = ANY($1::uuid[])`,
	`DELETE FROM credentials WHERE principal_id = ANY($1::uuid[])
	   AND id NOT IN (SELECT credential_id FROM audit_events WHERE credential_id IS NOT NULL)`,
	`DELETE FROM principal_identities WHERE principal_id = ANY($1::uuid[])`,
	`DELETE FROM principals WHERE id = ANY($1::uuid[])
	   AND id NOT IN (SELECT principal_id FROM audit_events WHERE principal_id IS NOT NULL)
	   AND NOT EXISTS (SELECT 1 FROM credentials c WHERE c.principal_id = principals.id)`,
}

func (a *App) purgeAccount(ctx context.Context, account string) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer dbx.Rollback(ctx, tx)

	var orphans []string
	if err = tx.QueryRow(ctx,
		`SELECT COALESCE(array_agg(principal_id::text),'{}') FROM (
			SELECT m.principal_id FROM account_memberships m
			WHERE m.account_id=$1
			GROUP BY m.principal_id
			HAVING NOT EXISTS (
				SELECT 1 FROM account_memberships other
				WHERE other.principal_id=m.principal_id AND other.account_id<>$1
			)
		) candidates`,
		account,
	).Scan(&orphans); err != nil {
		return err
	}

	for index, statement := range accountPurgeStatements {
		if _, err = tx.Exec(ctx, statement, account); err != nil {
			return fmt.Errorf("account statement %d: %w", index, err)
		}
	}
	if len(orphans) > 0 {
		for index, statement := range orphanPurgeStatements {
			if _, err = tx.Exec(ctx, statement, orphans); err != nil {
				return fmt.Errorf("orphan statement %d: %w", index, err)
			}
		}
	}
	return tx.Commit(ctx)
}
