package controlcmd

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/access"
	"github.com/thekiharani/monitor/internal/cloud/billing/paystack"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/control"
	"github.com/thekiharani/monitor/internal/cloud/postgres"
)

const usage = `usage: monitor-cloud control <group> <action> [arguments]

Commands:
  control accounts list [query]
  control accounts get <account-id>
  control accounts status <account-id> <active|suspended|closed> <reason>
  control usage list [account-id]
  control stats list
  control credentials list [account-id]
  control credentials revoke <credential-id> <reason>
  control credentials mint <name> <permission-set> <reason>
  control credentials bootstrap <name> <reason>
  control billing list [status]
  control billing checkouts [status]
  control billing events [status]
  control billing reconcile <account-id> <reason>
  control billing comp <account-id> <starter|pro|business> <yyyy-mm-dd-or-dash> <reason>
  control billing uncomp <account-id> <reason>
  control jobs list [pending|sent]
  control jobs retry <job-id> <reason>
  control access list [principal-id]
  control access grant <principal-id> <capability> <platform|account> <scope-id-or-dash> <reason>
  control access revoke <grant-id> <reason>
  control audit list [principal-or-target-id]
  control probes list
  control probes create <region> <name> <reason>
  control probes revoke <probe-id> <reason>

MONITOR_CONTROL_TOKEN is required. The credential must have audience control_api and
the capability required by the operation.

"credentials bootstrap" is the one-time exception: it provisions the first
operator credential and requires direct database access instead of a token.

Permission sets: platform.owner, support.operator, trust.reviewer,
billing.operator, platform.viewer.`

var argumentCounts = map[string]int{
	"access grant":          5,
	"access list":           0,
	"access revoke":         2,
	"accounts get":          1,
	"accounts list":         0,
	"accounts status":       3,
	"audit list":            0,
	"billing checkouts":     0,
	"billing comp":          4,
	"billing events":        0,
	"billing list":          0,
	"billing reconcile":     2,
	"billing uncomp":        2,
	"credentials bootstrap": 2,
	"credentials list":      0,
	"credentials mint":      3,
	"credentials revoke":    2,
	"jobs list":             0,
	"jobs retry":            2,
	"probes create":         3,
	"probes list":           0,
	"probes revoke":         2,
	"stats list":            0,
	"usage list":            0,
}

func Run(ctx context.Context, input []string, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "control:", err); return 1 }
	if len(input) == 1 && (input[0] == "help" || input[0] == "--help" || input[0] == "-h") {
		fmt.Fprintln(stdout, usage)
		return 0
	}
	if len(input) < 2 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	group, action := input[0], input[1]
	args := input[2:]
	count, known := argumentCounts[group+" "+action]
	if !known || len(args) < count {
		fmt.Fprintln(stderr, usage)
		return 2
	}

	c, err := config.Load()
	if err != nil {
		return fail(err)
	}
	db, err := postgres.Open(ctx, c)
	if err != nil {
		return fail(err)
	}
	defer db.Close()

	if group == "credentials" && action == "bootstrap" {
		minted, err := control.Bootstrap(ctx, db, args[0], join(args, 1))
		if err != nil {
			return fail(err)
		}
		if err := printResult(stdout, minted); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stderr, "bootstrap complete; set MONITOR_CONTROL_TOKEN to the returned token")
		return 0
	}

	principal, err := access.Authenticate(ctx, db, strings.TrimSpace(os.Getenv("MONITOR_CONTROL_TOKEN")), access.AudienceControlAPI)
	if err != nil {
		return fail(fmt.Errorf("control authentication failed: %w", err))
	}
	svc := control.New(db, paystack.New(c), c)
	actor := control.Actor{Principal: principal, UserAgent: "monitor-control-cli"}
	var result any
	switch group + " " + action {
	case "accounts list":
		result, _, err = svc.Accounts(ctx, actor, arg(args, 0), 500, 0)
	case "accounts get":
		result, err = svc.Account(ctx, actor, args[0])
	case "accounts status":
		err = svc.SetAccountStatus(ctx, actor, args[0], args[1], join(args, 2))
	case "usage list":
		result, _, err = svc.Usage(ctx, actor, arg(args, 0), 500, 0)
	case "stats list":
		result, err = svc.Stats(ctx, actor)
	case "credentials list":
		result, _, err = svc.Credentials(ctx, actor, arg(args, 0), 500, 0)
	case "credentials revoke":
		err = svc.RevokeCredential(ctx, actor, args[0], join(args, 1))
	case "credentials mint":
		result, err = svc.MintToken(ctx, actor, control.MintInput{Name: args[0], PermissionSet: args[1], Reason: join(args, 2)})
	case "billing list":
		result, _, err = svc.Collection(ctx, actor, "billing", arg(args, 0), 500, 0, "")
	case "billing checkouts":
		result, _, err = svc.Collection(ctx, actor, "checkouts", arg(args, 0), 500, 0, "")
	case "billing events":
		result, _, err = svc.Collection(ctx, actor, "events", arg(args, 0), 500, 0, "")
	case "billing reconcile":
		err = svc.ReconcileBilling(ctx, actor, args[0], join(args, 1))
	case "billing comp":
		expiresAt, parseErr := expiry(args[2])
		if parseErr != nil {
			return fail(parseErr)
		}
		err = svc.GrantComplimentaryPlan(ctx, actor, args[0], control.ComplimentaryInput{Plan: args[1], ExpiresAt: expiresAt, Reason: join(args, 3)})
	case "billing uncomp":
		err = svc.RevokeComplimentaryPlan(ctx, actor, args[0], join(args, 1))
	case "jobs list":
		result, _, err = svc.Collection(ctx, actor, "jobs", arg(args, 0), 500, 0, "")
	case "jobs retry":
		err = svc.RetryJob(ctx, actor, args[0], join(args, 1))
	case "access list":
		result, _, err = svc.Collection(ctx, actor, "grants", arg(args, 0), 500, 0, "")
	case "access grant":
		scopeID := args[3]
		if scopeID == "-" {
			scopeID = ""
		}
		err = svc.Grant(ctx, actor, control.GrantInput{PrincipalID: args[0], Capability: args[1], ScopeType: args[2], ScopeID: scopeID, Reason: join(args, 4)})
	case "access revoke":
		err = svc.RevokeGrant(ctx, actor, args[0], join(args, 1))
	case "audit list":
		result, _, err = svc.Collection(ctx, actor, "audit", arg(args, 0), 500, 0, "")
	case "probes list":
		result, _, err = svc.Probes(ctx, actor, 500, 0)
	case "probes create":
		result, err = svc.CreateProbe(ctx, actor, control.ProbeInput{Region: args[0], Name: args[1], Reason: join(args, 2)})
	case "probes revoke":
		err = svc.RevokeProbe(ctx, actor, args[0], join(args, 1))
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err != nil {
		return fail(err)
	}
	if err := printResult(stdout, result); err != nil {
		return fail(err)
	}
	return 0
}

func printResult(out io.Writer, result any) error {
	if result == nil {
		return nil
	}
	if err := json.MarshalWrite(out, result, jsontext.WithIndent("  ")); err != nil {
		return err
	}
	_, err := io.WriteString(out, "\n")
	return err
}

func expiry(value string) (string, error) {
	if value == "-" {
		return "", nil
	}
	day, err := time.Parse(time.DateOnly, value)
	if err != nil {
		return "", fmt.Errorf("expiry must be yyyy-mm-dd or -: %w", err)
	}
	return day.UTC().Format(time.RFC3339), nil
}

func arg(args []string, index int) string {
	if index >= len(args) {
		return ""
	}
	return args[index]
}
func join(args []string, index int) string {
	if index >= len(args) {
		return ""
	}
	return strings.Join(args[index:], " ")
}
