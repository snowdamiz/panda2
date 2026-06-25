package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

// AdminGuildBilling is the operator-facing billing snapshot for a single guild.
// It exposes both the raw stored status (for editing) and the effective status
// after grace/expiry rules are applied.
type AdminGuildBilling struct {
	HasSubscription    bool
	Plan               string
	PlanDisplayName    string
	Status             string
	StoredStatus       string
	GraceState         string
	PaymentProvider    string
	PeriodStart        time.Time
	PeriodEnd          time.Time
	TrialEndsAt        *time.Time
	CancelAtPeriodEnd  bool
	CanUsePaidFeatures bool
	ReadOnly           bool
	BillingOwnerUserID string
	Email              string
	Limits             PlanLimits
	Usage              repository.BillingUsageTotals
}

// AdminSetSubscriptionRequest captures an operator override of a guild
// subscription. Empty/nil fields leave the existing value untouched.
type AdminSetSubscriptionRequest struct {
	GuildID           string
	ActorUserID       string
	Plan              string
	Status            string
	PeriodEnd         *time.Time
	TrialEndsAt       *time.Time
	ClearTrialEndsAt  bool
	CancelAtPeriodEnd *bool
}

// AdminStatuses lists the subscription statuses an operator may assign.
func AdminStatuses() []string {
	return []string{StatusActive, StatusTrialing, StatusPastDue, StatusGrace, StatusReadOnly, StatusSuspended, StatusCanceled}
}

func isAdminStatus(status string) bool {
	for _, allowed := range AdminStatuses() {
		if status == allowed {
			return true
		}
	}
	return false
}

func graceForStatus(status string) string {
	switch status {
	case StatusActive:
		return GraceActive
	case StatusTrialing:
		return GraceTrialing
	case StatusPastDue:
		return GracePastDue
	case StatusGrace:
		return GraceGrace
	case StatusReadOnly:
		return GraceReadOnly
	case StatusSuspended:
		return GraceSuspended
	case StatusCanceled:
		return GraceCanceled
	default:
		return GraceReadOnly
	}
}

// AdminOverview composes the operator billing view for a guild. It never
// returns ErrNoSubscription; a guild without a subscription yields
// HasSubscription=false so the caller can still render the row.
func (s *Service) AdminOverview(ctx context.Context, guildID string) (AdminGuildBilling, error) {
	guildID = strings.TrimSpace(guildID)
	if s == nil || s.repo == nil {
		return AdminGuildBilling{}, ErrNoSubscription
	}
	if guildID == "" {
		return AdminGuildBilling{}, fmt.Errorf("guild id is required")
	}

	overview := AdminGuildBilling{}
	if account, ok, err := s.repo.GetCustomerAccountByGuild(ctx, guildID); err != nil {
		return AdminGuildBilling{}, err
	} else if ok {
		overview.Email = account.Email
		overview.BillingOwnerUserID = strings.TrimSpace(account.BillingOwnerUserID)
	}

	subscription, ok, err := s.repo.GetSubscriptionByGuild(ctx, guildID)
	if err != nil {
		return AdminGuildBilling{}, err
	}
	if !ok {
		return overview, nil
	}

	limits, ok := LimitsForPlan(subscription.Plan)
	if !ok {
		return AdminGuildBilling{}, ErrUnknownPlan
	}
	now := s.currentTime()
	status, grace, canUse, readOnly := effectiveState(subscription, now)
	totals, err := s.repo.UsageTotals(ctx, subscription.GuildID, subscription.CurrentPeriodStart.UTC(), subscription.CurrentPeriodEnd.UTC())
	if err != nil {
		return AdminGuildBilling{}, err
	}

	overview.HasSubscription = true
	overview.Plan = subscription.Plan
	overview.PlanDisplayName = limits.DisplayName
	overview.Status = status
	overview.StoredStatus = strings.TrimSpace(subscription.Status)
	overview.GraceState = grace
	overview.PaymentProvider = subscription.PaymentProvider
	overview.PeriodStart = subscription.CurrentPeriodStart.UTC()
	overview.PeriodEnd = subscription.CurrentPeriodEnd.UTC()
	overview.TrialEndsAt = subscription.TrialEndsAt
	overview.CancelAtPeriodEnd = subscription.CancelAtPeriodEnd
	overview.CanUsePaidFeatures = canUse
	overview.ReadOnly = readOnly
	overview.Limits = limits
	overview.Usage = totals
	if owner := strings.TrimSpace(subscription.BillingOwnerUserID); owner != "" {
		overview.BillingOwnerUserID = owner
	}
	return overview, nil
}

// AdminSetSubscription applies an operator override to a guild subscription,
// writing a fresh entitlement snapshot and recording an audit event. It creates
// a subscription if the guild does not yet have one.
func (s *Service) AdminSetSubscription(ctx context.Context, request AdminSetSubscriptionRequest) (AdminGuildBilling, error) {
	guildID := strings.TrimSpace(request.GuildID)
	if s == nil || s.repo == nil {
		return AdminGuildBilling{}, ErrNoSubscription
	}
	if guildID == "" {
		return AdminGuildBilling{}, fmt.Errorf("guild id is required")
	}

	now := s.currentTime()
	existing, hasExisting, err := s.repo.GetSubscriptionByGuild(ctx, guildID)
	if err != nil {
		return AdminGuildBilling{}, err
	}

	plan := ""
	if hasExisting {
		plan = existing.Plan
	}
	if requested := strings.TrimSpace(request.Plan); requested != "" {
		normalized, ok := NormalizePlan(requested)
		if !ok {
			return AdminGuildBilling{}, ErrUnknownPlan
		}
		plan = normalized
	}
	if plan == "" {
		plan = PlanTrial
	}
	limits, ok := LimitsForPlan(plan)
	if !ok {
		return AdminGuildBilling{}, ErrUnknownPlan
	}

	status := StatusActive
	if hasExisting && strings.TrimSpace(existing.Status) != "" {
		status = strings.TrimSpace(existing.Status)
	} else if plan == PlanTrial {
		status = StatusTrialing
	}
	if requested := strings.ToLower(strings.TrimSpace(request.Status)); requested != "" {
		if !isAdminStatus(requested) {
			return AdminGuildBilling{}, fmt.Errorf("unsupported status %q", requested)
		}
		status = requested
	}
	grace := graceForStatus(status)

	periodStart := now
	if hasExisting && !existing.CurrentPeriodStart.IsZero() {
		periodStart = existing.CurrentPeriodStart
	}
	periodEnd := time.Time{}
	if hasExisting {
		periodEnd = existing.CurrentPeriodEnd
	}
	if request.PeriodEnd != nil {
		periodEnd = request.PeriodEnd.UTC()
	}
	if periodEnd.IsZero() {
		if plan == PlanTrial {
			periodEnd = now.Add(TrialDuration)
		} else {
			periodEnd = now.AddDate(0, 1, 0)
		}
	}

	var trialEndsAt *time.Time
	if hasExisting {
		trialEndsAt = existing.TrialEndsAt
	}
	if request.ClearTrialEndsAt {
		trialEndsAt = nil
	} else if request.TrialEndsAt != nil {
		value := request.TrialEndsAt.UTC()
		trialEndsAt = &value
	}

	cancelAtPeriodEnd := false
	if hasExisting {
		cancelAtPeriodEnd = existing.CancelAtPeriodEnd
	}
	if request.CancelAtPeriodEnd != nil {
		cancelAtPeriodEnd = *request.CancelAtPeriodEnd
	}

	customerAccountID := uint(0)
	billingOwner := ""
	if hasExisting {
		customerAccountID = existing.CustomerAccountID
		billingOwner = existing.BillingOwnerUserID
	}
	if customerAccountID == 0 {
		account, err := s.repo.EnsureCustomerAccount(ctx, store.CustomerAccount{
			GuildID:            guildID,
			BillingOwnerUserID: billingOwner,
		})
		if err != nil {
			return AdminGuildBilling{}, err
		}
		customerAccountID = account.ID
		if billingOwner == "" {
			billingOwner = strings.TrimSpace(account.BillingOwnerUserID)
		}
	}

	externalSubscriptionID := ""
	externalEntitlementID := ""
	if hasExisting {
		externalSubscriptionID = existing.ExternalSubscriptionID
		externalEntitlementID = existing.ExternalEntitlementID
	}

	subscription := store.GuildSubscription{
		GuildID:                guildID,
		CustomerAccountID:      customerAccountID,
		Plan:                   plan,
		Status:                 status,
		GraceState:             grace,
		PaymentProvider:        ProviderManual,
		ExternalSubscriptionID: externalSubscriptionID,
		ExternalEntitlementID:  externalEntitlementID,
		BillingOwnerUserID:     billingOwner,
		CurrentPeriodStart:     periodStart,
		CurrentPeriodEnd:       periodEnd,
		TrialEndsAt:            trialEndsAt,
		CancelAtPeriodEnd:      cancelAtPeriodEnd,
	}
	if _, err := s.repo.UpsertSubscriptionWithSnapshot(ctx, subscription, snapshotForLimits(guildID, 0, limits, status, grace, now)); err != nil {
		return AdminGuildBilling{}, err
	}

	if s.audit != nil {
		metadata, err := json.Marshal(map[string]string{
			"plan":                 plan,
			"status":               status,
			"period_end":           periodEnd.Format(time.RFC3339),
			"cancel_at_period_end": strconv.FormatBool(cancelAtPeriodEnd),
		})
		if err != nil {
			metadata = []byte("{}")
		}
		_ = s.audit.Record(ctx, store.AuditEvent{
			GuildID:    guildID,
			ActorID:    strings.TrimSpace(request.ActorUserID),
			Action:     "admin.subscription.set",
			TargetType: "guild_subscription",
			TargetID:   guildID,
			Metadata:   string(metadata),
			CreatedAt:  now,
		})
	}

	return s.AdminOverview(ctx, guildID)
}
