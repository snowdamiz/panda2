package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

var (
	ErrNoSubscription        = errors.New("billing subscription is not configured")
	ErrReadOnly              = errors.New("billing account is read-only")
	ErrQuotaExceeded         = errors.New("billing quota exceeded")
	ErrUnknownPlan           = errors.New("unknown billing plan")
	ErrBillingAccess         = errors.New("billing can only be managed by the billing owner")
	ErrStripeNotConfigured   = errors.New("stripe billing is not configured")
	ErrStripeCustomerMissing = errors.New("stripe customer is not recorded for this guild")
)

type Config struct {
	PublicURL        string
	SuccessURL       string
	CancelURL        string
	StripeSecretKey  string
	StripeAPIBaseURL string
	DiscordSKUPlans  map[string]string
	StripePricePlans map[string]string
}

type Service struct {
	repo   *repository.BillingRepository
	cfg    Config
	stripe StripeClient
	now    func() time.Time
}

type TrialSeed struct {
	GuildID            string
	BillingOwnerUserID string
	Email              string
	TaxCountry         string
	SupportContact     string
	AuthorizedAt       time.Time
}

type Entitlement struct {
	GuildID            string
	SubscriptionID     uint
	Plan               PlanLimits
	Status             string
	GraceState         string
	PaymentProvider    string
	PeriodStart        time.Time
	PeriodEnd          time.Time
	TrialEndsAt        *time.Time
	CancelAtPeriodEnd  bool
	CanUsePaidFeatures bool
	ReadOnly           bool
	Usage              repository.BillingUsageTotals
	UpgradeURL         string
	PortalURL          string
}

type Reservation struct {
	ID          string
	GuildID     string
	Metric      string
	Units       int64
	Entitlement Entitlement
}

type QuotaError struct {
	Metric     string
	Used       int64
	Reserved   int64
	Limit      int64
	Plan       string
	UpgradeURL string
}

func (e QuotaError) Error() string {
	return fmt.Sprintf("%s quota exhausted", MetricLabel(e.Metric))
}

type DiscordEntitlementEvent struct {
	EventID        string
	EventType      string
	GuildID        string
	UserID         string
	SKUID          string
	EntitlementID  string
	SubscriptionID string
	Deleted        bool
	StartsAt       *time.Time
	EndsAt         *time.Time
	RawPayload     string
}

type StripeEvent struct {
	EventID            string
	EventType          string
	GuildID            string
	Plan               string
	CustomerEmail      string
	BillingOwnerUserID string
	CustomerID         string
	CheckoutSessionID  string
	SubscriptionID     string
	PriceID            string
	Status             string
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   time.Time
	CancelAtPeriodEnd  bool
	AmountCents        int64
	Currency           string
	RawPayload         string
}

type CheckoutKind string

const (
	CheckoutKindPlan CheckoutKind = "plan"
)

type CheckoutRequest struct {
	GuildID         string
	ActorUserID     string
	ActorIsOperator bool
	ActorCanClaim   bool
	Kind            CheckoutKind
	Plan            string
	CustomerEmail   string
}

type CheckoutSession struct {
	ID  string
	URL string
}

type PortalRequest struct {
	GuildID         string
	ActorUserID     string
	ActorIsOperator bool
}

type PortalSession struct {
	ID  string
	URL string
}

type CostEvent struct {
	GuildID             string
	RequestID           string
	Source              string
	Operation           string
	Command             string
	Provider            string
	Model               string
	PromptTokens        int
	CompletionTokens    int
	CachedInputTokens   int
	TotalTokens         int
	EstimatedCostMicros int64
	FinalCostMicros     int64
	Success             bool
	ErrorCode           string
}

func NewService(repo *repository.BillingRepository, cfg Config) *Service {
	cfg.PublicURL = strings.TrimRight(strings.TrimSpace(cfg.PublicURL), "/")
	cfg.SuccessURL = strings.TrimSpace(cfg.SuccessURL)
	cfg.CancelURL = strings.TrimSpace(cfg.CancelURL)
	cfg.StripeSecretKey = strings.TrimSpace(cfg.StripeSecretKey)
	cfg.StripeAPIBaseURL = strings.TrimRight(strings.TrimSpace(cfg.StripeAPIBaseURL), "/")
	if cfg.StripeAPIBaseURL == "" {
		cfg.StripeAPIBaseURL = defaultStripeAPIBaseURL
	}
	service := &Service{repo: repo, cfg: cfg, now: time.Now}
	if cfg.StripeSecretKey != "" {
		service.stripe = NewHTTPStripeClient(cfg.StripeSecretKey, cfg.StripeAPIBaseURL)
	}
	return service
}

func (s *Service) WithStripeClient(client StripeClient) *Service {
	s.stripe = client
	return s
}

func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) EnsureTrial(ctx context.Context, seed TrialSeed) (Entitlement, error) {
	if s == nil || s.repo == nil {
		return Entitlement{}, ErrNoSubscription
	}
	now := s.currentTime()
	if !seed.AuthorizedAt.IsZero() {
		now = seed.AuthorizedAt.UTC()
	}
	account, err := s.repo.EnsureCustomerAccount(ctx, store.CustomerAccount{
		GuildID:            seed.GuildID,
		BillingOwnerUserID: seed.BillingOwnerUserID,
		Email:              seed.Email,
		TaxCountry:         seed.TaxCountry,
		SupportContact:     seed.SupportContact,
	})
	if err != nil {
		return Entitlement{}, err
	}
	existing, ok, err := s.repo.GetSubscriptionByGuild(ctx, seed.GuildID)
	if err != nil {
		return Entitlement{}, err
	}
	if ok {
		return s.entitlementFromSubscription(ctx, existing)
	}
	limits := planLimits[PlanTrial]
	trialEnd := now.Add(TrialDuration)
	subscription, err := s.repo.UpsertSubscriptionWithSnapshot(ctx, store.GuildSubscription{
		GuildID:            strings.TrimSpace(seed.GuildID),
		CustomerAccountID:  account.ID,
		Plan:               PlanTrial,
		Status:             StatusTrialing,
		GraceState:         GraceTrialing,
		PaymentProvider:    ProviderTrial,
		BillingOwnerUserID: strings.TrimSpace(seed.BillingOwnerUserID),
		CurrentPeriodStart: now,
		CurrentPeriodEnd:   trialEnd,
		TrialEndsAt:        &trialEnd,
	}, snapshotForLimits("", 0, limits, StatusTrialing, GraceTrialing, now))
	if err != nil {
		return Entitlement{}, err
	}
	return s.entitlementFromSubscription(ctx, subscription)
}

func (s *Service) Resolve(ctx context.Context, guildID string) (Entitlement, error) {
	if s == nil || s.repo == nil {
		return Entitlement{}, ErrNoSubscription
	}
	subscription, ok, err := s.repo.GetSubscriptionByGuild(ctx, guildID)
	if err != nil {
		return Entitlement{}, err
	}
	if !ok {
		return Entitlement{GuildID: strings.TrimSpace(guildID), UpgradeURL: s.upgradeURL(guildID)}, ErrNoSubscription
	}
	return s.entitlementFromSubscription(ctx, subscription)
}

func (s *Service) CanManageBilling(ctx context.Context, guildID, userID string, operator bool) (bool, error) {
	if operator {
		return true, nil
	}
	guildID = strings.TrimSpace(guildID)
	userID = strings.TrimSpace(userID)
	if s == nil || s.repo == nil || guildID == "" || userID == "" {
		return false, nil
	}
	account, ok, err := s.repo.GetCustomerAccountByGuild(ctx, guildID)
	if err != nil {
		return false, err
	}
	if ok && strings.TrimSpace(account.BillingOwnerUserID) == userID {
		return true, nil
	}
	subscription, ok, err := s.repo.GetSubscriptionByGuild(ctx, guildID)
	if err != nil {
		return false, err
	}
	return ok && strings.TrimSpace(subscription.BillingOwnerUserID) == userID, nil
}

func (s *Service) CreateCheckoutSession(ctx context.Context, request CheckoutRequest) (CheckoutSession, error) {
	if s == nil || s.repo == nil || s.stripe == nil {
		return CheckoutSession{}, ErrStripeNotConfigured
	}
	request.GuildID = strings.TrimSpace(request.GuildID)
	request.ActorUserID = strings.TrimSpace(request.ActorUserID)
	request.CustomerEmail = strings.TrimSpace(request.CustomerEmail)
	if request.GuildID == "" {
		return CheckoutSession{}, fmt.Errorf("guild_id is required")
	}
	allowed, err := s.CanManageBilling(ctx, request.GuildID, request.ActorUserID, request.ActorIsOperator)
	if err != nil {
		return CheckoutSession{}, err
	}
	if !allowed && request.ActorCanClaim {
		claimable, err := s.billingOwnerUnclaimed(ctx, request.GuildID)
		if err != nil {
			return CheckoutSession{}, err
		}
		allowed = claimable
	}
	if !allowed {
		return CheckoutSession{}, ErrBillingAccess
	}

	account, ok, err := s.repo.GetCustomerAccountByGuild(ctx, request.GuildID)
	if err != nil {
		return CheckoutSession{}, err
	}
	if !ok {
		account, err = s.repo.EnsureCustomerAccount(ctx, store.CustomerAccount{
			GuildID:            request.GuildID,
			BillingOwnerUserID: request.ActorUserID,
			Email:              request.CustomerEmail,
			SupportContact:     request.CustomerEmail,
		})
		if err != nil {
			return CheckoutSession{}, err
		}
	}
	customerEmail := firstNonEmpty(request.CustomerEmail, account.Email)
	if request.ActorUserID == "" {
		request.ActorUserID = account.BillingOwnerUserID
	}

	metadata := map[string]string{
		"guild_id":              request.GuildID,
		"billing_owner_user_id": request.ActorUserID,
	}
	stripeMode := "subscription"
	var priceID string
	switch request.Kind {
	case CheckoutKindPlan, "":
		plan, ok := NormalizePlan(request.Plan)
		if !ok || plan == PlanTrial {
			return CheckoutSession{}, ErrUnknownPlan
		}
		priceID, err = s.stripePriceForPlan(plan)
		if err != nil {
			return CheckoutSession{}, err
		}
		metadata["kind"] = string(CheckoutKindPlan)
		metadata["plan"] = plan
	default:
		return CheckoutSession{}, fmt.Errorf("unsupported checkout kind")
	}

	session, err := s.stripe.CreateCheckoutSession(ctx, StripeCheckoutRequest{
		Mode:                 stripeMode,
		PriceID:              priceID,
		CustomerID:           account.StripeCustomerID,
		CustomerEmail:        customerEmail,
		ClientReferenceID:    request.GuildID,
		SuccessURL:           s.successURL(request.GuildID),
		CancelURL:            s.cancelURL(request.GuildID),
		Metadata:             metadata,
		SubscriptionMetadata: subscriptionMetadata(stripeMode, metadata),
	})
	if err != nil {
		return CheckoutSession{}, err
	}
	if session.CustomerID != "" && session.CustomerID != account.StripeCustomerID {
		_ = s.repo.SetStripeCustomerID(ctx, request.GuildID, session.CustomerID)
	}
	return CheckoutSession{ID: session.ID, URL: session.URL}, nil
}

func (s *Service) billingOwnerUnclaimed(ctx context.Context, guildID string) (bool, error) {
	account, accountOK, err := s.repo.GetCustomerAccountByGuild(ctx, guildID)
	if err != nil {
		return false, err
	}
	if accountOK && strings.TrimSpace(account.BillingOwnerUserID) != "" {
		return false, nil
	}
	subscription, subscriptionOK, err := s.repo.GetSubscriptionByGuild(ctx, guildID)
	if err != nil {
		return false, err
	}
	return !subscriptionOK || strings.TrimSpace(subscription.BillingOwnerUserID) == "", nil
}

func (s *Service) CreatePortalSession(ctx context.Context, request PortalRequest) (PortalSession, error) {
	if s == nil || s.repo == nil || s.stripe == nil {
		return PortalSession{}, ErrStripeNotConfigured
	}
	allowed, err := s.CanManageBilling(ctx, request.GuildID, request.ActorUserID, request.ActorIsOperator)
	if err != nil {
		return PortalSession{}, err
	}
	if !allowed {
		return PortalSession{}, ErrBillingAccess
	}
	account, ok, err := s.repo.GetCustomerAccountByGuild(ctx, request.GuildID)
	if err != nil {
		return PortalSession{}, err
	}
	if !ok || strings.TrimSpace(account.StripeCustomerID) == "" {
		return PortalSession{}, ErrStripeCustomerMissing
	}
	session, err := s.stripe.CreatePortalSession(ctx, StripePortalRequest{
		CustomerID: account.StripeCustomerID,
		ReturnURL:  s.returnURL(request.GuildID),
	})
	if err != nil {
		return PortalSession{}, err
	}
	return PortalSession{ID: session.ID, URL: session.URL}, nil
}

func (s *Service) Check(ctx context.Context, guildID, metric string, units int64) (Entitlement, error) {
	entitlement, err := s.Resolve(ctx, guildID)
	if err != nil {
		return entitlement, err
	}
	metric, ok := NormalizeMetric(metric)
	if !ok {
		return entitlement, fmt.Errorf("unsupported usage metric")
	}
	if units <= 0 {
		units = 1
	}
	if !entitlement.CanUsePaidFeatures || entitlement.ReadOnly {
		return entitlement, ErrReadOnly
	}
	limit := IncludedLimit(entitlement.Plan, metric)
	used := entitlement.metricConsumed(metric)
	reserved := entitlement.metricReserved(metric)
	if used+reserved+units > limit {
		return entitlement, QuotaError{Metric: metric, Used: used, Reserved: reserved, Limit: limit, Plan: entitlement.Plan.Plan, UpgradeURL: entitlement.UpgradeURL}
	}
	return entitlement, nil
}

func (s *Service) BeginUsage(ctx context.Context, guildID, metric string, units int64) (Reservation, error) {
	entitlement, err := s.Check(ctx, guildID, metric, units)
	if err != nil {
		return Reservation{GuildID: strings.TrimSpace(guildID), Metric: metric, Units: units, Entitlement: entitlement}, err
	}
	metric, _ = NormalizeMetric(metric)
	now := s.currentTime()
	reservation, totals, denied, err := s.repo.BeginUsageReservation(ctx, store.GuildSubscription{
		ID:                 entitlement.SubscriptionID,
		GuildID:            entitlement.GuildID,
		Plan:               entitlement.Plan.Plan,
		Status:             entitlement.Status,
		GraceState:         entitlement.GraceState,
		PaymentProvider:    entitlement.PaymentProvider,
		CurrentPeriodStart: entitlement.PeriodStart,
		CurrentPeriodEnd:   entitlement.PeriodEnd,
	}, metric, units, IncludedLimit(entitlement.Plan, metric), now)
	if err != nil {
		return Reservation{}, err
	}
	entitlement.Usage = totals
	if denied {
		used := entitlement.metricConsumed(metric)
		reserved := entitlement.metricReserved(metric)
		return Reservation{GuildID: entitlement.GuildID, Metric: metric, Units: units, Entitlement: entitlement}, QuotaError{
			Metric: metric, Used: used, Reserved: reserved, Limit: IncludedLimit(entitlement.Plan, metric), Plan: entitlement.Plan.Plan, UpgradeURL: entitlement.UpgradeURL,
		}
	}
	return Reservation{
		ID:          reservation.ReservationID,
		GuildID:     reservation.GuildID,
		Metric:      metric,
		Units:       units,
		Entitlement: entitlement,
	}, nil
}

func (s *Service) BeginCurrentUsage(ctx context.Context, guildID, metric string, currentUsed int64, units int64) (Reservation, error) {
	entitlement, err := s.Resolve(ctx, guildID)
	if err != nil {
		return Reservation{GuildID: strings.TrimSpace(guildID), Metric: metric, Units: units, Entitlement: entitlement}, err
	}
	metric, ok := NormalizeMetric(metric)
	if !ok {
		return Reservation{}, fmt.Errorf("unsupported usage metric")
	}
	if units <= 0 {
		units = 1
	}
	if currentUsed < 0 {
		currentUsed = 0
	}
	if !entitlement.CanUsePaidFeatures || entitlement.ReadOnly {
		return Reservation{GuildID: entitlement.GuildID, Metric: metric, Units: units, Entitlement: entitlement}, ErrReadOnly
	}
	now := s.currentTime()
	reservation, totals, denied, err := s.repo.BeginCurrentUsageReservation(ctx, store.GuildSubscription{
		ID:                 entitlement.SubscriptionID,
		GuildID:            entitlement.GuildID,
		Plan:               entitlement.Plan.Plan,
		Status:             entitlement.Status,
		GraceState:         entitlement.GraceState,
		PaymentProvider:    entitlement.PaymentProvider,
		CurrentPeriodStart: entitlement.PeriodStart,
		CurrentPeriodEnd:   entitlement.PeriodEnd,
	}, metric, units, currentUsed, IncludedLimit(entitlement.Plan, metric), now)
	if err != nil {
		return Reservation{}, err
	}
	entitlement.Usage = totals
	if denied {
		used := entitlement.metricConsumed(metric)
		reserved := entitlement.metricReserved(metric)
		return Reservation{GuildID: entitlement.GuildID, Metric: metric, Units: units, Entitlement: entitlement}, QuotaError{
			Metric: metric, Used: used, Reserved: reserved, Limit: IncludedLimit(entitlement.Plan, metric), Plan: entitlement.Plan.Plan, UpgradeURL: entitlement.UpgradeURL,
		}
	}
	return Reservation{
		ID:          reservation.ReservationID,
		GuildID:     reservation.GuildID,
		Metric:      metric,
		Units:       units,
		Entitlement: entitlement,
	}, nil
}

func (s *Service) SyncCurrentUsage(ctx context.Context, guildID, metric string, currentUsed int64) error {
	entitlement, err := s.Resolve(ctx, guildID)
	if err != nil {
		return err
	}
	metric, ok := NormalizeMetric(metric)
	if !ok {
		return fmt.Errorf("unsupported usage metric")
	}
	return s.repo.SyncCurrentUsage(ctx, store.GuildSubscription{
		ID:                 entitlement.SubscriptionID,
		GuildID:            entitlement.GuildID,
		Plan:               entitlement.Plan.Plan,
		Status:             entitlement.Status,
		GraceState:         entitlement.GraceState,
		PaymentProvider:    entitlement.PaymentProvider,
		CurrentPeriodStart: entitlement.PeriodStart,
		CurrentPeriodEnd:   entitlement.PeriodEnd,
	}, metric, currentUsed, s.currentTime())
}

func (s *Service) CommitUsage(ctx context.Context, reservation Reservation) error {
	if s == nil || s.repo == nil || strings.TrimSpace(reservation.ID) == "" {
		return nil
	}
	return s.repo.CommitUsageReservation(ctx, reservation.ID)
}

func (s *Service) ReleaseUsage(ctx context.Context, reservation Reservation) error {
	if s == nil || s.repo == nil || strings.TrimSpace(reservation.ID) == "" {
		return nil
	}
	return s.repo.ReleaseUsageReservation(ctx, reservation.ID)
}

func (s *Service) HandleDiscordEntitlement(ctx context.Context, event DiscordEntitlementEvent) error {
	if s == nil || s.repo == nil {
		return ErrNoSubscription
	}
	event.EventID = strings.TrimSpace(event.EventID)
	event.GuildID = strings.TrimSpace(event.GuildID)
	event.SKUID = strings.TrimSpace(event.SKUID)
	event.EntitlementID = strings.TrimSpace(event.EntitlementID)
	if event.EventID == "" {
		event.EventID = strings.Join([]string{ProviderDiscord, event.EventType, event.EntitlementID}, ":")
	}
	raw := firstNonEmpty(event.RawPayload, "{}")
	return s.repo.WithTransaction(ctx, func(repo *repository.BillingRepository) error {
		inserted, err := repo.RecordInvoicePaymentEvent(ctx, store.InvoicePaymentEvent{
			Provider:       ProviderDiscord,
			ExternalID:     event.EntitlementID,
			GuildID:        event.GuildID,
			Status:         strings.ToLower(strings.TrimSpace(event.EventType)),
			IdempotencyKey: event.EventID,
			RawPayload:     raw,
		})
		if err != nil || !inserted {
			return err
		}
		plan, planOK := s.cfg.DiscordSKUPlans[event.SKUID]
		if !planOK {
			return nil
		}
		if event.GuildID == "" {
			return nil
		}
		now := s.currentTime()
		plan, planOK = NormalizePlan(plan)
		if !planOK {
			return ErrUnknownPlan
		}
		if event.Deleted || strings.EqualFold(event.EventType, "delete") {
			return s.setGuildReadOnlyWithRepo(ctx, repo, event.GuildID, ProviderDiscord, event.EntitlementID, now)
		}
		periodStart := now
		if event.StartsAt != nil && !event.StartsAt.IsZero() {
			periodStart = event.StartsAt.UTC()
		}
		periodEnd := now.AddDate(0, 1, 0)
		if event.EndsAt != nil && !event.EndsAt.IsZero() {
			periodEnd = event.EndsAt.UTC()
		}
		status := StatusActive
		grace := GraceActive
		if event.EndsAt != nil && event.EndsAt.Before(now) {
			status = StatusReadOnly
			grace = GraceReadOnly
		}
		limits, _ := LimitsForPlan(plan)
		account, err := repo.EnsureCustomerAccount(ctx, store.CustomerAccount{
			GuildID:            event.GuildID,
			BillingOwnerUserID: event.UserID,
		})
		if err != nil {
			return err
		}
		_, err = repo.UpsertSubscriptionWithSnapshot(ctx, store.GuildSubscription{
			GuildID:                event.GuildID,
			CustomerAccountID:      account.ID,
			Plan:                   plan,
			Status:                 status,
			GraceState:             grace,
			PaymentProvider:        ProviderDiscord,
			ExternalSubscriptionID: event.SubscriptionID,
			ExternalEntitlementID:  event.EntitlementID,
			BillingOwnerUserID:     event.UserID,
			CurrentPeriodStart:     periodStart,
			CurrentPeriodEnd:       periodEnd,
		}, snapshotForLimits(event.GuildID, 0, limits, status, grace, now))
		return err
	})
}

func (s *Service) HandleStripeEvent(ctx context.Context, event StripeEvent) error {
	if s == nil || s.repo == nil {
		return ErrNoSubscription
	}
	event.EventID = strings.TrimSpace(event.EventID)
	event.GuildID = strings.TrimSpace(event.GuildID)
	event.Plan = strings.TrimSpace(event.Plan)
	event.PriceID = strings.TrimSpace(event.PriceID)
	event.SubscriptionID = strings.TrimSpace(event.SubscriptionID)
	event.CustomerID = strings.TrimSpace(event.CustomerID)
	event.CheckoutSessionID = strings.TrimSpace(event.CheckoutSessionID)
	if event.GuildID == "" && event.SubscriptionID != "" {
		if subscription, ok, err := s.repo.GetSubscriptionByExternalSubscriptionID(ctx, event.SubscriptionID); err != nil {
			return err
		} else if ok {
			event.GuildID = subscription.GuildID
			if event.BillingOwnerUserID == "" {
				event.BillingOwnerUserID = subscription.BillingOwnerUserID
			}
		}
	}
	raw := firstNonEmpty(event.RawPayload, "{}")
	externalID := firstNonEmpty(event.SubscriptionID, event.CheckoutSessionID)
	idempotencyKey := firstNonEmpty(event.EventID, ProviderStripe+":"+event.EventType+":"+externalID)
	return s.repo.WithTransaction(ctx, func(repo *repository.BillingRepository) error {
		inserted, err := repo.RecordInvoicePaymentEvent(ctx, store.InvoicePaymentEvent{
			Provider:       ProviderStripe,
			ExternalID:     externalID,
			GuildID:        event.GuildID,
			AmountCents:    event.AmountCents,
			Currency:       firstNonEmpty(event.Currency, "usd"),
			Status:         firstNonEmpty(event.Status, event.EventType),
			IdempotencyKey: idempotencyKey,
			RawPayload:     raw,
		})
		if err != nil || !inserted {
			return err
		}
		if event.GuildID != "" && event.CustomerID != "" {
			if _, err := repo.EnsureCustomerAccount(ctx, store.CustomerAccount{
				GuildID:            event.GuildID,
				BillingOwnerUserID: event.BillingOwnerUserID,
				Email:              event.CustomerEmail,
				SupportContact:     event.CustomerEmail,
				StripeCustomerID:   event.CustomerID,
			}); err != nil {
				return err
			}
		}
		plan, ok := s.stripeEventPlan(event)
		if !ok || event.GuildID == "" {
			return nil
		}
		now := s.currentTime()
		periodStart := event.CurrentPeriodStart
		if periodStart.IsZero() {
			periodStart = now
		}
		periodEnd := event.CurrentPeriodEnd
		if periodEnd.IsZero() || !periodEnd.After(periodStart) {
			periodEnd = periodStart.AddDate(0, 1, 0)
		}
		status, grace := statusFromStripe(event.Status, event.CancelAtPeriodEnd, periodEnd, now)
		limits, _ := LimitsForPlan(plan)
		account, err := repo.EnsureCustomerAccount(ctx, store.CustomerAccount{
			GuildID:            event.GuildID,
			BillingOwnerUserID: event.BillingOwnerUserID,
			Email:              event.CustomerEmail,
			SupportContact:     event.CustomerEmail,
			StripeCustomerID:   event.CustomerID,
		})
		if err != nil {
			return err
		}
		_, err = repo.UpsertSubscriptionWithSnapshot(ctx, store.GuildSubscription{
			GuildID:                event.GuildID,
			CustomerAccountID:      account.ID,
			Plan:                   plan,
			Status:                 status,
			GraceState:             grace,
			PaymentProvider:        ProviderStripe,
			ExternalSubscriptionID: event.SubscriptionID,
			BillingOwnerUserID:     event.BillingOwnerUserID,
			CurrentPeriodStart:     periodStart.UTC(),
			CurrentPeriodEnd:       periodEnd.UTC(),
			CancelAtPeriodEnd:      event.CancelAtPeriodEnd,
		}, snapshotForLimits(event.GuildID, 0, limits, status, grace, now))
		return err
	})
}

func (s *Service) RecordCost(ctx context.Context, event CostEvent) error {
	if s == nil || s.repo == nil {
		return nil
	}
	return s.repo.RecordCostLedgerEvent(ctx, store.CostLedgerEvent{
		GuildID:             event.GuildID,
		RequestID:           event.RequestID,
		Source:              event.Source,
		Operation:           event.Operation,
		Command:             event.Command,
		Provider:            event.Provider,
		Model:               event.Model,
		PromptTokens:        event.PromptTokens,
		CompletionTokens:    event.CompletionTokens,
		CachedInputTokens:   event.CachedInputTokens,
		TotalTokens:         event.TotalTokens,
		EstimatedCostMicros: event.EstimatedCostMicros,
		FinalCostMicros:     event.FinalCostMicros,
		Success:             event.Success,
		ErrorCode:           event.ErrorCode,
	})
}

func (s *Service) entitlementFromSubscription(ctx context.Context, subscription store.GuildSubscription) (Entitlement, error) {
	limits, ok := LimitsForPlan(subscription.Plan)
	if !ok {
		return Entitlement{}, ErrUnknownPlan
	}
	now := s.currentTime()
	status, grace, canUse, readOnly := effectiveState(subscription, now)
	totals, err := s.repo.UsageTotals(ctx, subscription.GuildID, subscription.CurrentPeriodStart.UTC(), subscription.CurrentPeriodEnd.UTC())
	if err != nil {
		return Entitlement{}, err
	}
	return Entitlement{
		GuildID:            subscription.GuildID,
		SubscriptionID:     subscription.ID,
		Plan:               limits,
		Status:             status,
		GraceState:         grace,
		PaymentProvider:    subscription.PaymentProvider,
		PeriodStart:        subscription.CurrentPeriodStart.UTC(),
		PeriodEnd:          subscription.CurrentPeriodEnd.UTC(),
		TrialEndsAt:        subscription.TrialEndsAt,
		CancelAtPeriodEnd:  subscription.CancelAtPeriodEnd,
		CanUsePaidFeatures: canUse,
		ReadOnly:           readOnly,
		Usage:              totals,
		UpgradeURL:         s.upgradeURL(subscription.GuildID),
		PortalURL:          s.portalURL(subscription.GuildID),
	}, nil
}

func (s *Service) setGuildReadOnly(ctx context.Context, guildID, provider, entitlementID string, now time.Time) error {
	return s.setGuildReadOnlyWithRepo(ctx, s.repo, guildID, provider, entitlementID, now)
}

func (s *Service) setGuildReadOnlyWithRepo(ctx context.Context, repo *repository.BillingRepository, guildID, provider, entitlementID string, now time.Time) error {
	subscription, ok, err := repo.GetSubscriptionByGuild(ctx, guildID)
	if err != nil || !ok {
		return err
	}
	limits, ok := LimitsForPlan(subscription.Plan)
	if !ok {
		return ErrUnknownPlan
	}
	_, err = repo.UpsertSubscriptionWithSnapshot(ctx, store.GuildSubscription{
		GuildID:                subscription.GuildID,
		CustomerAccountID:      subscription.CustomerAccountID,
		Plan:                   subscription.Plan,
		Status:                 StatusReadOnly,
		GraceState:             GraceReadOnly,
		PaymentProvider:        firstNonEmpty(provider, subscription.PaymentProvider),
		ExternalSubscriptionID: subscription.ExternalSubscriptionID,
		ExternalEntitlementID:  firstNonEmpty(entitlementID, subscription.ExternalEntitlementID),
		BillingOwnerUserID:     subscription.BillingOwnerUserID,
		CurrentPeriodStart:     subscription.CurrentPeriodStart,
		CurrentPeriodEnd:       firstFuture(subscription.CurrentPeriodEnd, now),
		TrialEndsAt:            subscription.TrialEndsAt,
		CancelAtPeriodEnd:      true,
	}, snapshotForLimits(guildID, subscription.ID, limits, StatusReadOnly, GraceReadOnly, now))
	return err
}

func snapshotForLimits(guildID string, subscriptionID uint, limits PlanLimits, status, grace string, now time.Time) store.EntitlementSnapshot {
	return store.EntitlementSnapshot{
		GuildID:                    guildID,
		SubscriptionID:             subscriptionID,
		Plan:                       limits.Plan,
		Status:                     status,
		GraceState:                 grace,
		AIResponsesLimit:           limits.AIResponses,
		WebSearchesLimit:           limits.WebSearches,
		KnowledgeStorageBytesLimit: limits.KnowledgeStorageBytes,
		SchedulesLimit:             limits.Schedules,
		RetentionDays:              limits.RetentionDays,
		MusicEnabled:               limits.MusicEnabled,
		PremiumToolsEnabled:        limits.PremiumToolsEnabled,
		CreatedAt:                  now,
	}
}

func effectiveState(subscription store.GuildSubscription, now time.Time) (status string, grace string, canUse bool, readOnly bool) {
	status = strings.TrimSpace(subscription.Status)
	grace = strings.TrimSpace(subscription.GraceState)
	if status == "" {
		status = StatusReadOnly
	}
	if grace == "" {
		grace = status
	}
	if subscription.CurrentPeriodEnd.IsZero() || !now.Before(subscription.CurrentPeriodEnd.Add(GraceDuration)) {
		switch status {
		case StatusActive, StatusPastDue, StatusGrace:
			return StatusSuspended, GraceSuspended, false, true
		case StatusTrialing:
			return StatusReadOnly, GraceReadOnly, false, true
		}
	}
	if now.After(subscription.CurrentPeriodEnd) && status == StatusPastDue {
		return StatusGrace, GraceGrace, true, false
	}
	switch status {
	case StatusTrialing, StatusActive, StatusGrace:
		return status, grace, true, false
	case StatusPastDue:
		return status, GracePastDue, true, false
	case StatusReadOnly, StatusCanceled, StatusSuspended:
		return status, grace, false, true
	default:
		return StatusReadOnly, GraceReadOnly, false, true
	}
}

func statusFromStripe(status string, cancelAtPeriodEnd bool, periodEnd, now time.Time) (string, string) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "trialing":
		if cancelAtPeriodEnd {
			return StatusActive, GraceActive
		}
		return StatusActive, GraceActive
	case "past_due", "unpaid":
		if now.Before(periodEnd.Add(GraceDuration)) {
			return StatusPastDue, GracePastDue
		}
		return StatusSuspended, GraceSuspended
	case "canceled", "cancelled", "incomplete_expired":
		return StatusCanceled, GraceCanceled
	default:
		return StatusReadOnly, GraceReadOnly
	}
}

func firstFuture(value time.Time, now time.Time) time.Time {
	if value.After(now) {
		return value
	}
	return now
}

func (s *Service) currentTime() time.Time {
	if s == nil || s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func (s *Service) upgradeURL(guildID string) string {
	base := s.returnURL(guildID)
	if base == "" {
		return ""
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := parsed.Query()
	if strings.TrimSpace(guildID) != "" {
		query.Set("guild_id", strings.TrimSpace(guildID))
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *Service) portalURL(guildID string) string {
	return s.returnURL(guildID)
}

func (s *Service) successURL(guildID string) string {
	base := firstNonEmpty(s.cfg.SuccessURL, s.publicPath("/billing/success"))
	return withBillingQuery(base, guildID, true)
}

func (s *Service) cancelURL(guildID string) string {
	base := firstNonEmpty(s.cfg.CancelURL, s.publicPath("/billing/cancel"))
	return withBillingQuery(base, guildID, false)
}

func (s *Service) returnURL(guildID string) string {
	return withBillingQuery(s.publicPath("/#pricing"), guildID, false)
}

func (s *Service) publicPath(path string) string {
	if strings.TrimSpace(s.cfg.PublicURL) == "" {
		return ""
	}
	if strings.HasPrefix(path, "/") {
		return s.cfg.PublicURL + path
	}
	return s.cfg.PublicURL + "/" + path
}

func withBillingQuery(base, guildID string, includeSessionPlaceholder bool) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := parsed.Query()
	if strings.TrimSpace(guildID) != "" {
		query.Set("guild_id", strings.TrimSpace(guildID))
	}
	if includeSessionPlaceholder {
		query.Set("session_id", "{CHECKOUT_SESSION_ID}")
	}
	rawQuery := query.Encode()
	if includeSessionPlaceholder {
		rawQuery = strings.ReplaceAll(rawQuery, "%7BCHECKOUT_SESSION_ID%7D", "{CHECKOUT_SESSION_ID}")
	}
	parsed.RawQuery = rawQuery
	return parsed.String()
}

func (s *Service) stripePriceForPlan(plan string) (string, error) {
	plan, ok := NormalizePlan(plan)
	if !ok || plan == PlanTrial {
		return "", ErrUnknownPlan
	}
	return uniqueMappedID(s.cfg.StripePricePlans, plan, "Stripe plan price")
}

func uniqueMappedID(values map[string]string, target string, label string) (string, error) {
	var matches []string
	for id, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			matches = append(matches, strings.TrimSpace(id))
		}
	}
	sort.Strings(matches)
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%s for %s is not configured", label, target)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%s for %s is ambiguous", label, target)
	}
}

func subscriptionMetadata(mode string, metadata map[string]string) map[string]string {
	if mode != "subscription" {
		return nil
	}
	result := map[string]string{}
	for key, value := range metadata {
		result[key] = value
	}
	return result
}

func (s *Service) stripeEventPlan(event StripeEvent) (string, bool) {
	if plan, ok := NormalizePlan(event.Plan); ok {
		return plan, true
	}
	plan, ok := s.cfg.StripePricePlans[event.PriceID]
	if !ok {
		return "", false
	}
	return NormalizePlan(plan)
}

func (e Entitlement) metricConsumed(metric string) int64 {
	switch metric {
	case MetricAIResponse:
		return e.Usage.AIResponsesConsumed
	case MetricWebSearch:
		return e.Usage.WebSearchesConsumed
	case MetricKnowledgeStorageByte:
		return e.Usage.KnowledgeStorageBytesConsumed
	case MetricScheduledRun:
		return e.Usage.ScheduledRunsConsumed
	case MetricMusicMinute:
		return e.Usage.MusicMinutesConsumed
	default:
		return 0
	}
}

func (e Entitlement) metricReserved(metric string) int64 {
	switch metric {
	case MetricAIResponse:
		return e.Usage.AIResponsesReserved
	case MetricWebSearch:
		return e.Usage.WebSearchesReserved
	case MetricKnowledgeStorageByte:
		return e.Usage.KnowledgeStorageBytesReserved
	case MetricScheduledRun:
		return e.Usage.ScheduledRunsReserved
	case MetricMusicMinute:
		return e.Usage.MusicMinutesReserved
	default:
		return 0
	}
}

func (e Entitlement) UsageLine(metric string) string {
	metric, _ = NormalizeMetric(metric)
	limit := IncludedLimit(e.Plan, metric)
	return FormatUsage(e.metricConsumed(metric)+e.metricReserved(metric), limit, metric)
}

func (e Entitlement) SummaryText() string {
	return strings.Join([]string{
		fmt.Sprintf("Plan: %s (%s)", e.Plan.DisplayName, e.Status),
		fmt.Sprintf("Renewal/reset: %s", e.PeriodEnd.Format("2006-01-02")),
		fmt.Sprintf("AI responses: %s", e.UsageLine(MetricAIResponse)),
		fmt.Sprintf("Web searches: %s", e.UsageLine(MetricWebSearch)),
		fmt.Sprintf("Knowledge storage: %s", e.UsageLine(MetricKnowledgeStorageByte)),
		fmt.Sprintf("Retention: %d days", e.Plan.RetentionDays),
	}, "\n")
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func MarshalRaw(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}
