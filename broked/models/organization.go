package models

import (
	"fmt"
	"strings"
	"time"
)

const (
	AccountStatusTrial     = "trial"
	AccountStatusActive    = "active"
	AccountStatusSuspended = "suspended"
	AccountStatusChurned   = "churned"
)

// Organization represents a company/tenant in the multi-tenant system.
type Organization struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`              // URL-safe: acme -> acme.orkestri.site
	Plan          string    `json:"plan"`              // free, team, enterprise
	MaxPipelines  int       `json:"max_pipelines"`     // 0 = unlimited
	MaxRunsPerDay int       `json:"max_runs_per_day"`  // 0 = unlimited
	MaxStorageMB  int       `json:"max_storage_mb"`    // 0 = unlimited
	MaxMembers    int       `json:"max_members"`       // 0 = unlimited
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`

	ContactEmail    string     `json:"contact_email"`
	BillingEmail    string     `json:"billing_email"`
	CompanySize     string     `json:"company_size"`                // "1-10", "11-50", "51-200", "201-1000", "1000+"
	Industry        string     `json:"industry"`
	Country         string     `json:"country"`
	Timezone        string     `json:"timezone"`
	LogoURL         string     `json:"logo_url"`
	Phone           string     `json:"phone"`
	Notes           string     `json:"notes,omitempty"`             // ops-only, never returned to customers
	AccountStatus   string     `json:"account_status"`              // trial, active, suspended, churned
	TrialStartsAt   *time.Time `json:"trial_starts_at,omitempty"`
	TrialEndsAt     *time.Time `json:"trial_ends_at,omitempty"`
	PlanStartedAt   *time.Time `json:"plan_started_at,omitempty"`
	SuspendedAt     *time.Time `json:"suspended_at,omitempty"`
	SuspendedReason string     `json:"suspended_reason,omitempty"`

	// Billing & Subscription
	BillingCycle      string     `json:"billing_cycle"`                 // "monthly", "yearly", "" (trial/free)
	SubscriptionEndsAt *time.Time `json:"subscription_ends_at,omitempty"` // when current billing period ends
	AutoRenew         bool       `json:"auto_renew"`                    // auto-renew subscription
	WarningsSent      int        `json:"warnings_sent"`                 // how many expiry warnings sent (0-3)
	LastWarningAt     *time.Time `json:"last_warning_at,omitempty"`     // when last warning was sent
	StripeCustomerID  string     `json:"stripe_customer_id,omitempty"`  // future: Stripe integration
	StripeSubID       string     `json:"stripe_sub_id,omitempty"`       // future: Stripe subscription ID
}

// OrgMember represents a user's membership in an organization.
type OrgMember struct {
	OrgID    string    `json:"org_id"`
	UserID   string    `json:"user_id"`
	Username string    `json:"username"`
	Role     string    `json:"role"` // owner, admin, member
	JoinedAt time.Time `json:"joined_at"`
}

// DefaultOrganization returns the default org for single-tenant/self-hosted deployments.
func DefaultOrganization() *Organization {
	return &Organization{
		ID:            "default",
		Name:          "Default Organization",
		Slug:          "default",
		Plan:          "enterprise",
		AccountStatus: AccountStatusActive,
	}
}

// Validate checks organization fields.
func (o *Organization) Validate() error {
	if o.Name == "" {
		return fmt.Errorf("organization name is required")
	}
	// Prevent XSS
	if strings.ContainsAny(o.Name, "<>\"'&") {
		return fmt.Errorf("organization name contains invalid characters")
	}
	if o.Slug == "" {
		return fmt.Errorf("organization slug is required")
	}
	// Slug must be URL-safe
	for _, c := range o.Slug {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return fmt.Errorf("slug must contain only lowercase letters, numbers, and hyphens")
		}
	}
	if len(o.Slug) < 2 || len(o.Slug) > 50 {
		return fmt.Errorf("slug must be 2-50 characters")
	}
	if o.AccountStatus != "" {
		switch o.AccountStatus {
		case AccountStatusTrial, AccountStatusActive, AccountStatusSuspended, AccountStatusChurned:
			// valid
		default:
			return fmt.Errorf("invalid account_status: %s", o.AccountStatus)
		}
	}
	return nil
}

// Sanitize returns a copy of the organization with ops-only fields zeroed out.
func (o Organization) Sanitize() Organization {
	o.Notes = ""
	o.SuspendedReason = ""
	return o
}
