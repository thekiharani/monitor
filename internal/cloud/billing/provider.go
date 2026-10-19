package billing

import (
	"context"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/billing/paystack"
)

type Provider interface {
	FetchPlan(ctx context.Context, code string) (paystack.Plan, error)
	InitializeCurrency(ctx context.Context, email, reference, callback, accountID, checkoutID, plan, providerPlanCode, currency string, amount int64, channels []string) (paystack.InitializeResult, error)
	Initialize(ctx context.Context, email, reference, callback, accountID, checkoutID string) (paystack.InitializeResult, error)
	InitializeOneTime(ctx context.Context, email, reference, callback, accountID, checkoutID, plan string, amount int64) (paystack.InitializeResult, error)
	InitializeOneTimeChannels(ctx context.Context, email, reference, callback, accountID, checkoutID, plan string, amount int64, channels []string) (paystack.InitializeResult, error)
	Verify(ctx context.Context, reference string) (paystack.Transaction, error)
	FetchSubscription(ctx context.Context, code string) (paystack.Subscription, error)
	ListSubscriptions(ctx context.Context, customerID int64) ([]paystack.Subscription, error)
	ManagementLink(ctx context.Context, code string) (string, error)
	DisableSubscription(ctx context.Context, code, token string) error
	EnableSubscription(ctx context.Context, code, token string) error
	CreateSubscription(ctx context.Context, customer, planCode string, startDate time.Time) (paystack.CreatedSubscription, error)
}
