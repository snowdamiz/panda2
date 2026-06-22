package billing

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	storepkg "github.com/sn0w/panda2/internal/store"
)

func TestEnsureTrialMetersUsageAndDeniesOverQuota(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })

	entitlement, err := service.EnsureTrial(ctx, TrialSeed{
		GuildID:            "guild-1",
		BillingOwnerUserID: "owner-1",
		AuthorizedAt:       now,
	})
	if err != nil {
		t.Fatalf("EnsureTrial: %v", err)
	}
	if entitlement.Plan.Plan != PlanTrial || entitlement.Status != StatusTrialing || !entitlement.CanUsePaidFeatures || entitlement.ReadOnly {
		t.Fatalf("unexpected trial entitlement: %+v", entitlement)
	}
	if entitlement.PeriodEnd.Sub(entitlement.PeriodStart) != TrialDuration {
		t.Fatalf("expected %s trial window, got %s", TrialDuration, entitlement.PeriodEnd.Sub(entitlement.PeriodStart))
	}

	reservation, err := service.BeginUsage(ctx, "guild-1", MetricAIResponse, int64(entitlement.Plan.AIResponses))
	if err != nil {
		t.Fatalf("BeginUsage at trial limit: %v", err)
	}
	if reservation.ID == "" {
		t.Fatal("expected reservation id")
	}
	if err := service.CommitUsage(ctx, reservation); err != nil {
		t.Fatalf("CommitUsage: %v", err)
	}

	_, err = service.BeginUsage(ctx, "guild-1", MetricAIResponse, 1)
	var quotaErr QuotaError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("expected QuotaError after trial limit, got %T %v", err, err)
	}
	if quotaErr.Metric != MetricAIResponse || quotaErr.Used != int64(entitlement.Plan.AIResponses) || quotaErr.Limit != int64(entitlement.Plan.AIResponses) {
		t.Fatalf("unexpected quota error: %+v", quotaErr)
	}

	resolved, err := service.Resolve(ctx, "guild-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Usage.AIResponsesConsumed != int64(entitlement.Plan.AIResponses) || resolved.Usage.AIResponsesReserved != 0 {
		t.Fatalf("unexpected persisted usage totals: %+v", resolved.Usage)
	}
}

func TestStripeWebhookIsIdempotentAndGrantsMappedPlan(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	event := StripeEvent{
		EventID:            "evt_1",
		EventType:          "customer.subscription.updated",
		GuildID:            "guild-1",
		BillingOwnerUserID: "owner-1",
		CustomerEmail:      "owner@example.com",
		SubscriptionID:     "sub_1",
		PriceID:            "price_plus",
		Status:             "active",
		CurrentPeriodStart: now,
		CurrentPeriodEnd:   now.AddDate(0, 1, 0),
		AmountCents:        4900,
		Currency:           "usd",
		RawPayload:         `{"id":"evt_1"}`,
	}
	if err := service.HandleStripeEvent(ctx, event); err != nil {
		t.Fatalf("HandleStripeEvent first call: %v", err)
	}
	if err := service.HandleStripeEvent(ctx, event); err != nil {
		t.Fatalf("HandleStripeEvent duplicate call: %v", err)
	}

	entitlement, err := service.Resolve(ctx, "guild-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if entitlement.Plan.Plan != PlanPlus || entitlement.Status != StatusActive || !entitlement.CanUsePaidFeatures {
		t.Fatalf("unexpected Stripe entitlement: %+v", entitlement)
	}
	if entitlement.UpgradeURL != "https://panda.example/?guild_id=guild-1#pricing" || entitlement.PortalURL != "https://panda.example/?guild_id=guild-1#pricing" {
		t.Fatalf("unexpected billing links: upgrade=%q portal=%q", entitlement.UpgradeURL, entitlement.PortalURL)
	}

	var events int64
	if err := database.DB.Model(&storepkg.InvoicePaymentEvent{}).Count(&events).Error; err != nil {
		t.Fatalf("count invoice events: %v", err)
	}
	if events != 1 {
		t.Fatalf("expected one idempotent invoice event, got %d", events)
	}

	var snapshots int64
	if err := database.DB.Model(&storepkg.EntitlementSnapshot{}).Where("guild_id = ?", "guild-1").Count(&snapshots).Error; err != nil {
		t.Fatalf("count entitlement snapshots: %v", err)
	}
	if snapshots != 1 {
		t.Fatalf("expected one entitlement snapshot after duplicate event, got %d", snapshots)
	}
}

func TestCreatePlanCheckoutSessionUsesStripeMetadata(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()
	stripe := &fakeStripeClient{
		checkout: StripeCheckoutSession{ID: "cs_plan", URL: "https://checkout.stripe.test/plan"},
	}
	service.WithStripeClient(stripe)

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	if _, err := service.EnsureTrial(ctx, TrialSeed{GuildID: "guild-1", BillingOwnerUserID: "owner-1", AuthorizedAt: now}); err != nil {
		t.Fatalf("EnsureTrial: %v", err)
	}

	session, err := service.CreateCheckoutSession(ctx, CheckoutRequest{
		GuildID:       "guild-1",
		ActorUserID:   "owner-1",
		Kind:          CheckoutKindPlan,
		Plan:          PlanPlus,
		CustomerEmail: "owner@example.com",
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if session.URL != "https://checkout.stripe.test/plan" || stripe.checkoutCalls != 1 {
		t.Fatalf("unexpected checkout session=%+v calls=%d", session, stripe.checkoutCalls)
	}
	request := stripe.lastCheckout
	if request.Mode != "subscription" || request.PriceID != "price_plus" || request.CustomerEmail != "owner@example.com" {
		t.Fatalf("unexpected Stripe plan request: %+v", request)
	}
	if request.Metadata["guild_id"] != "guild-1" || request.Metadata["billing_owner_user_id"] != "owner-1" || request.Metadata["plan"] != PlanPlus || request.Metadata["kind"] != string(CheckoutKindPlan) {
		t.Fatalf("unexpected plan metadata: %+v", request.Metadata)
	}
	if request.SubscriptionMetadata["plan"] != PlanPlus || request.SubscriptionMetadata["guild_id"] != "guild-1" {
		t.Fatalf("expected subscription metadata copy, got %+v", request.SubscriptionMetadata)
	}
	if !strings.Contains(request.SuccessURL, "/billing/success") || !strings.Contains(request.SuccessURL, "guild_id=guild-1") || !strings.Contains(request.SuccessURL, "session_id={CHECKOUT_SESSION_ID}") {
		t.Fatalf("unexpected success url %q", request.SuccessURL)
	}
	if !strings.Contains(request.CancelURL, "/billing/cancel") || !strings.Contains(request.CancelURL, "guild_id=guild-1") {
		t.Fatalf("unexpected cancel url %q", request.CancelURL)
	}
}

func TestUnclaimedGuildAdminCanCreateFirstCheckout(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()
	stripe := &fakeStripeClient{
		checkout: StripeCheckoutSession{ID: "cs_first", URL: "https://checkout.stripe.test/first"},
	}
	service.WithStripeClient(stripe)

	session, err := service.CreateCheckoutSession(ctx, CheckoutRequest{
		GuildID:       "guild-1",
		ActorUserID:   "admin-1",
		ActorCanClaim: true,
		Kind:          CheckoutKindPlan,
		Plan:          PlanStarter,
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if session.ID != "cs_first" {
		t.Fatalf("unexpected first checkout session: %+v", session)
	}
	var account storepkg.CustomerAccount
	if err := database.DB.Where("guild_id = ?", "guild-1").First(&account).Error; err != nil {
		t.Fatalf("load claimed account: %v", err)
	}
	if account.BillingOwnerUserID != "admin-1" {
		t.Fatalf("expected first checkout actor to become billing owner, got %+v", account)
	}
}

func TestCreatePortalSessionUsesStoredStripeCustomer(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()
	stripe := &fakeStripeClient{
		portal: StripePortalSession{ID: "bps_1", URL: "https://billing.stripe.test/session"},
	}
	service.WithStripeClient(stripe)

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	if _, err := service.EnsureTrial(ctx, TrialSeed{GuildID: "guild-1", BillingOwnerUserID: "owner-1", AuthorizedAt: now}); err != nil {
		t.Fatalf("EnsureTrial: %v", err)
	}
	if _, err := service.CreatePortalSession(ctx, PortalRequest{GuildID: "guild-1", ActorUserID: "owner-1"}); !errors.Is(err, ErrStripeCustomerMissing) {
		t.Fatalf("expected missing customer error before checkout, got %v", err)
	}
	if err := database.DB.Model(&storepkg.CustomerAccount{}).Where("guild_id = ?", "guild-1").Update("stripe_customer_id", "cus_1").Error; err != nil {
		t.Fatalf("store customer id: %v", err)
	}
	session, err := service.CreatePortalSession(ctx, PortalRequest{GuildID: "guild-1", ActorUserID: "owner-1"})
	if err != nil {
		t.Fatalf("CreatePortalSession: %v", err)
	}
	if session.URL != "https://billing.stripe.test/session" || stripe.lastPortal.CustomerID != "cus_1" {
		t.Fatalf("unexpected portal session=%+v request=%+v", session, stripe.lastPortal)
	}
	if !strings.Contains(stripe.lastPortal.ReturnURL, "guild_id=guild-1") {
		t.Fatalf("unexpected portal return url %q", stripe.lastPortal.ReturnURL)
	}
}

func TestPastDueGraceAllowsUseThenSuspendsAfterGraceWindow(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	graceEvent := StripeEvent{
		EventID:            "evt_grace",
		EventType:          "invoice.payment_failed",
		GuildID:            "guild-1",
		BillingOwnerUserID: "owner-1",
		SubscriptionID:     "sub_1",
		PriceID:            "price_starter",
		Status:             "past_due",
		CurrentPeriodStart: now.AddDate(0, -1, 0),
		CurrentPeriodEnd:   now.Add(-time.Hour),
		RawPayload:         `{"id":"evt_grace"}`,
	}
	if err := service.HandleStripeEvent(ctx, graceEvent); err != nil {
		t.Fatalf("HandleStripeEvent grace: %v", err)
	}
	entitlement, err := service.Check(ctx, "guild-1", MetricWebSearch, 1)
	if err != nil {
		t.Fatalf("expected grace-period use to be allowed, got %v", err)
	}
	if entitlement.Status != StatusGrace || entitlement.GraceState != GraceGrace || !entitlement.CanUsePaidFeatures || entitlement.ReadOnly {
		t.Fatalf("unexpected grace entitlement: %+v", entitlement)
	}

	suspendedEvent := graceEvent
	suspendedEvent.EventID = "evt_suspended"
	suspendedEvent.CurrentPeriodEnd = now.Add(-(GraceDuration + time.Hour))
	if err := service.HandleStripeEvent(ctx, suspendedEvent); err != nil {
		t.Fatalf("HandleStripeEvent suspended: %v", err)
	}
	entitlement, err = service.Check(ctx, "guild-1", MetricWebSearch, 1)
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("expected read-only after grace window, got entitlement=%+v err=%v", entitlement, err)
	}
	if entitlement.Status != StatusSuspended || entitlement.GraceState != GraceSuspended || !entitlement.ReadOnly {
		t.Fatalf("unexpected suspended entitlement: %+v", entitlement)
	}
}

func TestRecordCostPersistsInternalProviderDetailsOnlyInLedger(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	err := service.RecordCost(ctx, CostEvent{
		GuildID:             "guild-1",
		RequestID:           "req-1",
		Source:              "assistant",
		Operation:           "answer",
		Command:             "chat",
		Provider:            "internal-provider",
		Model:               "internal-model",
		PromptTokens:        100,
		CompletionTokens:    20,
		TotalTokens:         120,
		EstimatedCostMicros: 42,
		Success:             true,
	})
	if err != nil {
		t.Fatalf("RecordCost: %v", err)
	}

	var event storepkg.CostLedgerEvent
	if err := database.DB.Where("guild_id = ? AND request_id = ?", "guild-1", "req-1").First(&event).Error; err != nil {
		t.Fatalf("load cost ledger event: %v", err)
	}
	if event.Provider != "internal-provider" || event.Model != "internal-model" || event.EstimatedCostMicros != 42 {
		t.Fatalf("unexpected cost ledger event: %+v", event)
	}
}

func newBillingTestService(t *testing.T) (*Service, *storepkg.Store) {
	t.Helper()
	ctx := context.Background()
	database, err := storepkg.Open(ctx, filepath.Join(t.TempDir(), "billing.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	service := NewService(repository.NewBillingRepository(database.DB), Config{
		PublicURL: "https://panda.example",
		DiscordSKUPlans: map[string]string{
			"sku_starter": PlanStarter,
		},
		StripePricePlans: map[string]string{
			"price_starter": PlanStarter,
			"price_plus":    PlanPlus,
		},
	})
	return service, database
}

type fakeStripeClient struct {
	checkoutCalls int
	portalCalls   int
	lastCheckout  StripeCheckoutRequest
	lastPortal    StripePortalRequest
	checkout      StripeCheckoutSession
	portal        StripePortalSession
	err           error
}

func (f *fakeStripeClient) CreateCheckoutSession(_ context.Context, request StripeCheckoutRequest) (StripeCheckoutSession, error) {
	f.checkoutCalls++
	f.lastCheckout = request
	if f.err != nil {
		return StripeCheckoutSession{}, f.err
	}
	return f.checkout, nil
}

func (f *fakeStripeClient) CreatePortalSession(_ context.Context, request StripePortalRequest) (StripePortalSession, error) {
	f.portalCalls++
	f.lastPortal = request
	if f.err != nil {
		return StripePortalSession{}, f.err
	}
	return f.portal, nil
}
