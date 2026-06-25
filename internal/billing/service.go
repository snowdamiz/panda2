package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

var (
	ErrNoSubscription           = errors.New("billing subscription is not configured")
	ErrReadOnly                 = errors.New("billing account is read-only")
	ErrQuotaExceeded            = errors.New("billing quota exceeded")
	ErrUnknownPlan              = errors.New("unknown billing plan")
	ErrBillingAccess            = errors.New("billing can only be managed by the billing owner")
	ErrSolPaymentsNotConfigured = errors.New("sol payments are not configured")
)

type Config struct {
	PublicURL              string
	Environment            string
	SolanaRPCURL           string
	SolanaCluster          string
	SolanaTreasuryWallet   string
	SolanaConfirmation     string
	SolanaPlanLamports     map[string]int64
	SolanaOrderExpiration  time.Duration
	SolanaActivationKeyTTL time.Duration
}

type Service struct {
	repo   *repository.BillingRepository
	audit  *repository.AuditRepository
	cfg    Config
	solana SolanaRPCClient
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
	cfg.Environment = strings.ToLower(strings.TrimSpace(cfg.Environment))
	cfg.SolanaRPCURL = strings.TrimSpace(cfg.SolanaRPCURL)
	cfg.SolanaCluster = strings.TrimSpace(cfg.SolanaCluster)
	if cfg.SolanaCluster == "" {
		cfg.SolanaCluster = "devnet"
	}
	cfg.SolanaTreasuryWallet = strings.TrimSpace(cfg.SolanaTreasuryWallet)
	cfg.SolanaConfirmation = strings.ToLower(strings.TrimSpace(cfg.SolanaConfirmation))
	if cfg.SolanaConfirmation == "" {
		cfg.SolanaConfirmation = "finalized"
	}
	if cfg.SolanaOrderExpiration <= 0 {
		cfg.SolanaOrderExpiration = 30 * time.Minute
	}
	if cfg.SolanaActivationKeyTTL <= 0 {
		cfg.SolanaActivationKeyTTL = 48 * time.Hour
	}
	cfg.SolanaPlanLamports = normalizePlanLamports(cfg.SolanaPlanLamports)
	service := &Service{repo: repo, cfg: cfg, now: time.Now}
	if cfg.SolanaRPCURL != "" {
		service.solana = NewHTTPSolanaRPCClient(cfg.SolanaRPCURL)
	}
	return service
}

func (s *Service) WithSolanaRPCClient(client SolanaRPCClient) *Service {
	s.solana = client
	return s
}

func (s *Service) WithAuditRecorder(audit *repository.AuditRepository) *Service {
	s.audit = audit
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
	if s.developmentMode() {
		return s.developmentEntitlement(seed.GuildID), nil
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
	if s.developmentMode() {
		return s.developmentEntitlement(guildID), nil
	}
	subscription, ok, err := s.repo.GetSubscriptionByGuild(ctx, guildID)
	if err != nil {
		return Entitlement{}, err
	}
	if !ok {
		return s.EnsureTrial(ctx, TrialSeed{
			GuildID:      guildID,
			AuthorizedAt: s.currentTime(),
		})
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
	if unlimitedLimit(limit) {
		return entitlement, nil
	}
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
	if s.developmentMode() {
		return Reservation{GuildID: entitlement.GuildID, Metric: metric, Units: units, Entitlement: entitlement}, nil
	}
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
	if s.developmentMode() {
		return Reservation{GuildID: entitlement.GuildID, Metric: metric, Units: units, Entitlement: entitlement}, nil
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
	if s.developmentMode() {
		return nil
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

func (s *Service) developmentMode() bool {
	return s != nil && strings.TrimSpace(s.cfg.Environment) != "" && !strings.EqualFold(strings.TrimSpace(s.cfg.Environment), "production")
}

func (s *Service) developmentEntitlement(guildID string) Entitlement {
	now := s.currentTime()
	return Entitlement{
		GuildID: strings.TrimSpace(guildID),
		Plan: PlanLimits{
			Plan:                  PlanDevelopment,
			DisplayName:           "Development",
			AIResponses:           int(UnlimitedUsageLimit),
			WebSearches:           int(UnlimitedUsageLimit),
			KnowledgeStorageBytes: UnlimitedUsageLimit,
			Schedules:             int(UnlimitedUsageLimit),
			RetentionDays:         365,
			MusicEnabled:          true,
			PremiumToolsEnabled:   true,
		},
		Status:             StatusActive,
		GraceState:         GraceActive,
		PaymentProvider:    ProviderManual,
		PeriodStart:        now,
		PeriodEnd:          now.AddDate(100, 0, 0),
		CanUsePaidFeatures: true,
		UpgradeURL:         s.upgradeURL(guildID),
	}
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
		fmt.Sprintf("Provider: %s", firstNonEmpty(e.PaymentProvider, "unknown")),
		fmt.Sprintf("Renewal/reset: %s", e.PeriodEnd.Format("2006-01-02")),
		fmt.Sprintf("AI responses: %s", e.UsageLine(MetricAIResponse)),
		fmt.Sprintf("Web searches: %s", e.UsageLine(MetricWebSearch)),
		fmt.Sprintf("Knowledge storage: %s", e.UsageLine(MetricKnowledgeStorageByte)),
		fmt.Sprintf("Retention: %d days", e.Plan.RetentionDays),
	}, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func MarshalRaw(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}
