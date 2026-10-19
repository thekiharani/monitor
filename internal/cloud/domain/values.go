package domain

const (
	PrincipalKindHuman   = "human"
	PrincipalKindService = "service"

	PrincipalStatusActive    = "active"
	PrincipalStatusSuspended = "suspended"

	AccountStatusActive    = "active"
	AccountStatusSuspended = "suspended"
	AccountStatusClosed    = "closed"

	PlanFree     = "free"
	PlanStarter  = "starter"
	PlanPro      = "pro"
	PlanBusiness = "business"

	MonitorKindHTTP      = "http"
	MonitorKindKeyword   = "keyword"
	MonitorKindTCP       = "tcp"
	MonitorKindHeartbeat = "heartbeat"

	MonitorStatePending = "pending"
	MonitorStateUp      = "up"
	MonitorStateDown    = "down"
	MonitorStatePaused  = "paused"

	MembershipRoleOwner  = "owner"
	MembershipRoleAdmin  = "admin"
	MembershipRoleMember = "member"

	CredentialKindSession = "session"
	CredentialKindAPIKey  = "api_key"

	CredentialAudienceCustomerSession = "customer_session"
	CredentialAudienceCustomerAPI     = "customer_api"
	CredentialAudienceControlAPI      = "control_api"

	CredentialAssuranceSingleFactor = "single_factor"
	CredentialAssuranceMultiFactor  = "multi_factor"
	CredentialAssuranceWorkload     = "workload"

	ScopeTypePlatform = "platform"
	ScopeTypeAccount  = "account"

	CheckoutTypeSubscription = "subscription"
	CheckoutTypeUpgrade      = "upgrade"
	CheckoutTypeRenewal      = "renewal"

	BillingModeAuto   = "auto"
	BillingModeManual = "manual"
	BillingModeComp   = "comp"

	SubscriptionStatusActive      = "active"
	SubscriptionStatusPastDue     = "past_due"
	SubscriptionStatusNonRenewing = "non_renewing"
	SubscriptionStatusCanceled    = "canceled"
	SubscriptionStatusComplete    = "complete"

	CheckoutStatusPending     = "pending"
	CheckoutStatusInitialized = "initialized"
	CheckoutStatusSuccessful  = "successful"
	CheckoutStatusFailed      = "failed"
	CheckoutStatusExpired     = "expired"

	BillingEventStatusReceived  = "received"
	BillingEventStatusProcessed = "processed"
	BillingEventStatusIgnored   = "ignored"
	BillingEventStatusFailed    = "failed"
)

func IsPrincipalKind(value string) bool {
	return value == PrincipalKindHuman || value == PrincipalKindService
}
func IsPrincipalStatus(value string) bool {
	return value == PrincipalStatusActive || value == PrincipalStatusSuspended
}
func IsAccountStatus(value string) bool {
	return value == AccountStatusActive || value == AccountStatusSuspended || value == AccountStatusClosed
}
func IsPlan(value string) bool {
	return value == PlanFree || value == PlanStarter || value == PlanPro || value == PlanBusiness
}
func IsPaidPlan(value string) bool {
	return value == PlanStarter || value == PlanPro || value == PlanBusiness
}
func IsMonitorKind(value string) bool {
	return value == MonitorKindHTTP || value == MonitorKindKeyword || value == MonitorKindTCP || value == MonitorKindHeartbeat
}
func IsMembershipRole(value string) bool {
	return value == MembershipRoleOwner || value == MembershipRoleAdmin || value == MembershipRoleMember
}
func IsAssignableRole(value string) bool {
	return value == MembershipRoleAdmin || value == MembershipRoleMember
}
func CanManageMembers(role string) bool {
	return role == MembershipRoleOwner || role == MembershipRoleAdmin
}
func CanManageWorkspace(role string) bool {
	return role == MembershipRoleOwner || role == MembershipRoleAdmin
}
func CanManageBilling(role string) bool {
	return role == MembershipRoleOwner
}
func IsBillingMode(value string) bool {
	return value == BillingModeAuto || value == BillingModeManual || value == BillingModeComp
}
func IsCredentialKind(value string) bool {
	return value == CredentialKindSession || value == CredentialKindAPIKey
}
func IsCredentialAudience(value string) bool {
	return value == CredentialAudienceCustomerSession || value == CredentialAudienceCustomerAPI || value == CredentialAudienceControlAPI
}
func IsCredentialAssurance(value string) bool {
	return value == CredentialAssuranceSingleFactor || value == CredentialAssuranceMultiFactor || value == CredentialAssuranceWorkload
}
func IsScopeType(value string) bool { return value == ScopeTypePlatform || value == ScopeTypeAccount }
func IsCheckoutType(value string) bool {
	return value == CheckoutTypeSubscription || value == CheckoutTypeUpgrade || value == CheckoutTypeRenewal
}

func IsSubscriptionStatus(value string) bool {
	return value == SubscriptionStatusActive || value == SubscriptionStatusPastDue || value == SubscriptionStatusNonRenewing || value == SubscriptionStatusCanceled || value == SubscriptionStatusComplete
}

func IsCheckoutStatus(value string) bool {
	return value == CheckoutStatusPending || value == CheckoutStatusInitialized || value == CheckoutStatusSuccessful || value == CheckoutStatusFailed || value == CheckoutStatusExpired
}

func IsBillingEventStatus(value string) bool {
	return value == BillingEventStatusReceived || value == BillingEventStatusProcessed || value == BillingEventStatusIgnored || value == BillingEventStatusFailed
}
