package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

var (
	ErrNoCreditAccount          = errors.New("billing credit account is not configured")
	ErrReadOnly                 = errors.New("billing credit account is read-only")
	ErrCreditsDepleted          = errors.New("billing credits depleted")
	ErrUnknownPack              = errors.New("unknown billing pack")
	ErrBillingAccess            = errors.New("billing can only be managed by the billing owner")
	ErrSolPaymentsNotConfigured = errors.New("sol payments are not configured")
	ErrSolPriceNotConfigured    = errors.New("SOL/USD price is not configured")
)

type Config struct {
	PublicURL              string
	SolanaRPCURL           string
	SolanaCluster          string
	SolanaTreasuryWallet   string
	SolanaConfirmation     string
	SolanaPlanLamports     map[string]int64
	SolanaPackLamports     map[string]int64
	SolanaUSDCentsPerSOL   int64
	SolanaOrderExpiration  time.Duration
	SolanaActivationKeyTTL time.Duration
}

type Service struct {
	repo                 *repository.BillingRepository
	audit                *repository.AuditRepository
	cfg                  Config
	solana               SolanaRPCClient
	now                  func() time.Time
	solSubmissionLocks   [64]sync.Mutex
	solVerificationLocks [64]sync.Mutex
}

func (s *Service) solSubmissionLock(orderID string) *sync.Mutex {
	return solOrderLock(&s.solSubmissionLocks, orderID)
}

func (s *Service) solVerificationLock(orderID string) *sync.Mutex {
	return solOrderLock(&s.solVerificationLocks, orderID)
}

func solOrderLock(locks *[64]sync.Mutex, orderID string) *sync.Mutex {
	hash := uint32(2166136261)
	for index := 0; index < len(orderID); index++ {
		hash ^= uint32(orderID[index])
		hash *= 16777619
	}
	return &locks[hash%uint32(len(locks))]
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
	GuildID                    string
	AccountID                  uint
	Pack                       PackDefinition
	Status                     string
	GraceState                 string
	PaymentProvider            string
	PeriodStart                time.Time
	PeriodEnd                  time.Time
	TrialEndsAt                *time.Time
	CancelAtPeriodEnd          bool
	CanUsePaidFeatures         bool
	ReadOnly                   bool
	AvailableCredits           int64
	ReservedCredits            int64
	RetentionDays              int
	KnowledgeStorageBytesLimit int64
	StorageRentGraceUntil      *time.Time
	UpgradeURL                 string
}

type Reservation struct {
	ID          string
	GuildID     string
	Action      string
	Metric      string
	Units       int64
	Credits     int64
	MaxCredits  int64
	RequestID   string
	Entitlement Entitlement
}

type CreditError struct {
	Metric           string
	Action           string
	Used             int64
	Reserved         int64
	Limit            int64
	Pack             string
	RequiredCredits  int64
	AvailableCredits int64
	UpgradeURL       string
}

func (e CreditError) Error() string {
	action := firstNonEmpty(e.Action, e.Metric)
	if action == "" {
		action = "paid action"
	}
	return fmt.Sprintf("%s needs %d credits", ActionLabel(action), e.RequiredCredits)
}

type CostEvent struct {
	GuildID             string
	RequestID           string
	ReservationID       string
	Action              string
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

type CreditUsageFinal struct {
	Credits             int64
	FinalCostMicros     int64
	EstimatedCostMicros int64
	Metadata            map[string]any
}

func NewService(repo *repository.BillingRepository, cfg Config) *Service {
	cfg.PublicURL = strings.TrimRight(strings.TrimSpace(cfg.PublicURL), "/")
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
	cfg.SolanaPackLamports = normalizePackLamports(firstLamportsMap(cfg.SolanaPackLamports, cfg.SolanaPlanLamports))
	cfg.SolanaPlanLamports = cfg.SolanaPackLamports
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
		return Entitlement{}, ErrNoCreditAccount
	}
	now := s.currentTime()
	if !seed.AuthorizedAt.IsZero() {
		now = seed.AuthorizedAt.UTC()
	}
	pack := packDefinitions[PackTrial]
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
	trialEnd := now.Add(pack.ExpiresAfter)
	creditAccount, err := s.repo.EnsureCreditAccount(ctx, store.CreditAccount{
		GuildID:                    strings.TrimSpace(seed.GuildID),
		BillingOwnerUserID:         firstNonEmpty(seed.BillingOwnerUserID, account.BillingOwnerUserID),
		Status:                     StatusTrialing,
		PaymentProvider:            ProviderTrial,
		ActivePack:                 PackTrial,
		RetentionDays:              pack.RetentionDays,
		KnowledgeStorageBytesLimit: pack.KnowledgeStorageBytes,
		CreatedAt:                  now,
		UpdatedAt:                  now,
	})
	if err != nil {
		return Entitlement{}, err
	}
	_, creditAccount, _, err = s.repo.GrantCredits(ctx, repository.CreditGrantRequest{
		GrantID:      "grant_trial_" + safeLedgerID(seed.GuildID),
		GuildID:      seed.GuildID,
		Source:       ProviderTrial,
		SourceID:     "trial:" + strings.TrimSpace(seed.GuildID),
		Pack:         PackTrial,
		Credits:      pack.Credits,
		ExpiresAt:    &trialEnd,
		MetadataJSON: MarshalRaw(map[string]any{"pack": PackTrial, "source": ProviderTrial}),
	})
	if err != nil {
		return Entitlement{}, err
	}
	return s.entitlementFromCreditAccount(creditAccount), nil
}

func (s *Service) EnsureTrialIfMissing(ctx context.Context, seed TrialSeed) (Entitlement, bool, error) {
	if s == nil || s.repo == nil {
		return Entitlement{}, false, ErrNoCreditAccount
	}
	existing, ok, err := s.repo.GetCreditAccountByGuild(ctx, seed.GuildID)
	if err != nil {
		return Entitlement{}, false, err
	}
	if ok {
		return s.entitlementFromCreditAccount(existing), false, nil
	}
	entitlement, err := s.EnsureTrial(ctx, seed)
	return entitlement, err == nil, err
}

func (s *Service) Resolve(ctx context.Context, guildID string) (Entitlement, error) {
	if s == nil || s.repo == nil {
		return Entitlement{}, ErrNoCreditAccount
	}
	guildID = strings.TrimSpace(guildID)
	if _, err := s.repo.ExpireCreditGrantsForGuild(ctx, guildID, s.currentTime()); err != nil {
		return Entitlement{GuildID: guildID, UpgradeURL: s.upgradeURL(guildID)}, err
	}
	account, ok, err := s.repo.GetCreditAccountByGuild(ctx, guildID)
	if err != nil {
		return Entitlement{}, err
	}
	if !ok {
		return Entitlement{GuildID: guildID, UpgradeURL: s.upgradeURL(guildID)}, ErrNoCreditAccount
	}
	return s.entitlementFromCreditAccount(account), nil
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
	account, ok, err := s.repo.GetCreditAccountByGuild(ctx, guildID)
	if err != nil {
		return false, err
	}
	if ok && strings.TrimSpace(account.BillingOwnerUserID) == userID {
		return true, nil
	}
	customer, ok, err := s.repo.GetCustomerAccountByGuild(ctx, guildID)
	if err != nil {
		return false, err
	}
	return ok && strings.TrimSpace(customer.BillingOwnerUserID) == userID, nil
}

func (s *Service) billingOwnerUnclaimed(ctx context.Context, guildID string) (bool, error) {
	account, ok, err := s.repo.GetCreditAccountByGuild(ctx, guildID)
	if err != nil {
		return false, err
	}
	if ok && strings.TrimSpace(account.BillingOwnerUserID) != "" {
		return false, nil
	}
	customer, customerOK, err := s.repo.GetCustomerAccountByGuild(ctx, guildID)
	if err != nil {
		return false, err
	}
	return !customerOK || strings.TrimSpace(customer.BillingOwnerUserID) == "", nil
}

func (s *Service) QuoteAction(request ActionQuoteRequest) (CreditQuote, error) {
	return quoteForAction(request)
}

func (s *Service) Check(ctx context.Context, guildID, metric string, units int64) (Entitlement, error) {
	entitlement, err := s.Resolve(ctx, guildID)
	if err != nil {
		return entitlement, err
	}
	quote, err := quoteFromMetric(metric, units, "")
	if err != nil {
		return entitlement, err
	}
	if !entitlement.CanUsePaidFeatures || entitlement.ReadOnly {
		if entitlement.Depleted() {
			return entitlement, creditError(entitlement, quote, metric)
		}
		return entitlement, ErrReadOnly
	}
	if entitlement.AvailableCredits < quote.MaxCredits {
		return entitlement, creditError(entitlement, quote, metric)
	}
	return entitlement, nil
}

func (s *Service) BeginUsage(ctx context.Context, guildID, metric string, units int64) (Reservation, error) {
	quote, err := quoteFromMetric(metric, units, "")
	if err != nil {
		return Reservation{GuildID: strings.TrimSpace(guildID), Metric: metric, Units: units}, err
	}
	return s.BeginCreditUsage(ctx, guildID, quote)
}

func (s *Service) BeginCurrentUsage(ctx context.Context, guildID, metric string, currentUsed int64, units int64) (Reservation, error) {
	quote, err := quoteFromMetric(metric, units, "")
	if err != nil {
		return Reservation{GuildID: strings.TrimSpace(guildID), Metric: metric, Units: units}, err
	}
	quote.Metadata["current_used"] = currentUsed
	return s.BeginCreditUsage(ctx, guildID, quote)
}

func (s *Service) BeginCreditUsage(ctx context.Context, guildID string, quote CreditQuote) (Reservation, error) {
	if s == nil || s.repo == nil {
		return Reservation{}, ErrNoCreditAccount
	}
	entitlement, err := s.Resolve(ctx, guildID)
	if err != nil {
		return Reservation{GuildID: strings.TrimSpace(guildID), Action: quote.Action, Credits: quote.ExpectedCredits, MaxCredits: quote.MaxCredits, Entitlement: entitlement}, err
	}
	if !entitlement.CanUsePaidFeatures || entitlement.ReadOnly {
		if entitlement.Depleted() {
			return Reservation{GuildID: entitlement.GuildID, Action: quote.Action, Credits: quote.ExpectedCredits, MaxCredits: quote.MaxCredits, Entitlement: entitlement}, creditError(entitlement, quote, "")
		}
		return Reservation{GuildID: entitlement.GuildID, Action: quote.Action, Credits: quote.ExpectedCredits, MaxCredits: quote.MaxCredits, Entitlement: entitlement}, ErrReadOnly
	}
	if entitlement.AvailableCredits < quote.MaxCredits {
		return Reservation{GuildID: entitlement.GuildID, Action: quote.Action, Credits: quote.ExpectedCredits, MaxCredits: quote.MaxCredits, Entitlement: entitlement}, creditError(entitlement, quote, "")
	}
	now := s.currentTime()
	reservationID, err := randomBase58(18)
	if err != nil {
		return Reservation{}, err
	}
	stored, account, denied, err := s.repo.BeginCreditReservation(ctx, repository.CreditReservationRequest{
		ReservationID:   "crr_" + reservationID,
		GuildID:         entitlement.GuildID,
		Action:          quote.Action,
		RequestID:       quote.RequestID,
		ExpectedCredits: quote.ExpectedCredits,
		MaxCredits:      quote.MaxCredits,
		ExpiresAt:       now.Add(30 * time.Minute),
		MetadataJSON:    MarshalRaw(quote.Metadata),
	}, now)
	if err != nil {
		return Reservation{}, err
	}
	entitlement = s.entitlementFromCreditAccount(account)
	if denied {
		return Reservation{GuildID: entitlement.GuildID, Action: quote.Action, Credits: quote.ExpectedCredits, MaxCredits: quote.MaxCredits, Entitlement: entitlement}, creditError(entitlement, quote, "")
	}
	return Reservation{
		ID:          stored.ReservationID,
		GuildID:     stored.GuildID,
		Action:      stored.Action,
		Metric:      stored.Action,
		Units:       quote.ExpectedCredits,
		Credits:     quote.ExpectedCredits,
		MaxCredits:  quote.MaxCredits,
		RequestID:   quote.RequestID,
		Entitlement: entitlement,
	}, nil
}

func (s *Service) SyncCurrentUsage(ctx context.Context, guildID, metric string, currentUsed int64) error {
	if metric == ActionStorageRent {
		return s.ChargeStorageRent(ctx, guildID, currentUsed)
	}
	return nil
}

func (s *Service) CommitUsage(ctx context.Context, reservation Reservation) error {
	return s.CommitCreditUsage(ctx, reservation, CreditUsageFinal{})
}

func (s *Service) CommitCreditUsage(ctx context.Context, reservation Reservation, final CreditUsageFinal) error {
	if s == nil || s.repo == nil || strings.TrimSpace(reservation.ID) == "" {
		return nil
	}
	metadata := final.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	if final.FinalCostMicros > 0 {
		metadata["final_cost_micros"] = final.FinalCostMicros
	}
	if final.EstimatedCostMicros > 0 {
		metadata["estimated_cost_micros"] = final.EstimatedCostMicros
	}
	return s.repo.CommitCreditReservation(ctx, repository.CreditCommitRequest{
		ReservationID: reservation.ID,
		FinalCredits:  final.Credits,
		MetadataJSON:  MarshalRaw(metadata),
	}, s.currentTime())
}

func (s *Service) ReleaseUsage(ctx context.Context, reservation Reservation) error {
	return s.ReleaseCreditUsage(ctx, reservation, "released")
}

func (s *Service) ReleaseCreditUsage(ctx context.Context, reservation Reservation, reason string) error {
	if s == nil || s.repo == nil || strings.TrimSpace(reservation.ID) == "" {
		return nil
	}
	return s.repo.ReleaseCreditReservation(ctx, reservation.ID, reason, s.currentTime())
}

func (s *Service) GrantPack(ctx context.Context, guildID, source, sourceID, packID string) (Entitlement, error) {
	if s == nil || s.repo == nil {
		return Entitlement{}, ErrNoCreditAccount
	}
	pack, ok := PackForID(packID)
	if !ok {
		return Entitlement{}, ErrUnknownPack
	}
	now := s.currentTime()
	expiresAt := now.Add(pack.ExpiresAfter)
	account, err := s.repo.EnsureCreditAccount(ctx, store.CreditAccount{
		GuildID:                    guildID,
		Status:                     statusForPack(pack.Pack),
		PaymentProvider:            source,
		ActivePack:                 pack.Pack,
		RetentionDays:              pack.RetentionDays,
		KnowledgeStorageBytesLimit: pack.KnowledgeStorageBytes,
		CreatedAt:                  now,
		UpdatedAt:                  now,
	})
	if err != nil {
		return Entitlement{}, err
	}
	grantID, err := randomBase58(18)
	if err != nil {
		return Entitlement{}, err
	}
	_, account, _, err = s.repo.GrantCredits(ctx, repository.CreditGrantRequest{
		GrantID:      "grant_" + grantID,
		GuildID:      guildID,
		Source:       source,
		SourceID:     sourceID,
		Pack:         pack.Pack,
		Credits:      pack.Credits,
		ExpiresAt:    &expiresAt,
		MetadataJSON: MarshalRaw(map[string]any{"pack": pack.Pack, "source": source, "source_id": sourceID}),
	})
	if err != nil {
		return Entitlement{}, err
	}
	return s.entitlementFromCreditAccount(account), nil
}

func (s *Service) ExpireCredits(ctx context.Context, now time.Time) (int64, error) {
	if s == nil || s.repo == nil {
		return 0, nil
	}
	released, err := s.repo.ExpireCreditReservations(ctx, now)
	if err != nil {
		return released, err
	}
	expired, err := s.repo.ExpireCreditGrants(ctx, now)
	return released + expired, err
}

func (s *Service) ChargeStorageRent(ctx context.Context, guildID string, bytes int64) error {
	if bytes <= 0 {
		return nil
	}
	entitlement, err := s.Resolve(ctx, guildID)
	if err != nil {
		return err
	}
	if entitlement.StorageRentGraceUntil != nil && s.currentTime().Before(entitlement.StorageRentGraceUntil.UTC()) {
		return nil
	}
	quote, err := s.QuoteAction(ActionQuoteRequest{Action: ActionStorageRent, Bytes: bytes})
	if err != nil {
		return err
	}
	reservation, err := s.BeginCreditUsage(ctx, guildID, quote)
	if err != nil {
		return err
	}
	return s.CommitCreditUsage(ctx, reservation, CreditUsageFinal{Credits: quote.ExpectedCredits})
}

func (s *Service) RecordCost(ctx context.Context, event CostEvent) error {
	if s == nil || s.repo == nil {
		return nil
	}
	action := strings.TrimSpace(event.Action)
	if action == "" {
		action = strings.TrimSpace(event.Operation)
	}
	return s.repo.RecordCostLedgerEvent(ctx, store.CostLedgerEvent{
		GuildID:             event.GuildID,
		RequestID:           event.RequestID,
		ReservationID:       event.ReservationID,
		Action:              action,
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

func (s *Service) entitlementFromCreditAccount(account store.CreditAccount) Entitlement {
	pack, ok := PackForID(firstNonEmpty(account.ActivePack, PackTrial))
	if !ok {
		pack = packDefinitions[PackTrial]
	}
	status, grace, canUse, readOnly := effectiveCreditState(account, s.currentTime())
	periodStart := account.CreatedAt.UTC()
	periodEnd := periodStart.Add(pack.ExpiresAfter)
	var trialEndsAt *time.Time
	if pack.Pack == PackTrial {
		trialEndsAt = &periodEnd
	}
	return Entitlement{
		GuildID:                    account.GuildID,
		AccountID:                  account.ID,
		Pack:                       pack,
		Status:                     status,
		GraceState:                 grace,
		PaymentProvider:            account.PaymentProvider,
		PeriodStart:                periodStart,
		PeriodEnd:                  periodEnd,
		TrialEndsAt:                trialEndsAt,
		CanUsePaidFeatures:         canUse,
		ReadOnly:                   readOnly,
		AvailableCredits:           account.AvailableCredits,
		ReservedCredits:            account.ReservedCredits,
		RetentionDays:              account.RetentionDays,
		KnowledgeStorageBytesLimit: account.KnowledgeStorageBytesLimit,
		StorageRentGraceUntil:      account.StorageRentGraceUntil,
		UpgradeURL:                 s.upgradeURL(account.GuildID),
	}
}

func effectiveCreditState(account store.CreditAccount, now time.Time) (status string, grace string, canUse bool, readOnly bool) {
	status = strings.TrimSpace(account.Status)
	if status == "" {
		status = StatusReadOnly
	}
	grace = status
	switch status {
	case StatusTrialing, StatusActive, StatusGrace, StatusPastDue:
		if account.AvailableCredits <= 0 && account.ReservedCredits <= 0 {
			return StatusDepleted, GraceDepleted, false, true
		}
		return status, grace, true, false
	case StatusDepleted:
		if account.AvailableCredits > 0 || account.ReservedCredits > 0 {
			return StatusActive, GraceActive, true, false
		}
		return StatusDepleted, GraceDepleted, false, true
	case StatusReadOnly, StatusCanceled, StatusSuspended:
		return status, grace, false, true
	default:
		return StatusReadOnly, GraceReadOnly, false, true
	}
}

func quoteFromMetric(metric string, units int64, requestID string) (CreditQuote, error) {
	action, ok := NormalizeMetric(metric)
	if !ok {
		return CreditQuote{}, fmt.Errorf("unsupported usage action")
	}
	if units <= 0 {
		units = 1
	}
	request := ActionQuoteRequest{Action: action, RequestID: requestID, Metadata: map[string]any{"legacy_metric": metric, "legacy_units": units}}
	switch action {
	case ActionAssistantModelRound:
		quote, err := quoteForAction(request)
		if err != nil {
			return CreditQuote{}, err
		}
		quote.ExpectedCredits *= units
		quote.MaxCredits *= units
		quote.Metadata["expected_credits"] = quote.ExpectedCredits
		quote.Metadata["max_credits"] = quote.MaxCredits
		return quote, nil
	case ActionKnowledgeWrite:
		request.Bytes = units
	case ActionMusicPlayback:
		if units > math.MaxInt32 {
			units = math.MaxInt32
		}
		request.Minutes = int(units)
	}
	return quoteForAction(request)
}

func creditError(entitlement Entitlement, quote CreditQuote, metric string) CreditError {
	return CreditError{
		Metric:           metric,
		Action:           quote.Action,
		Used:             entitlement.Pack.Credits - entitlement.AvailableCredits,
		Reserved:         entitlement.ReservedCredits,
		Limit:            entitlement.Pack.Credits,
		Pack:             entitlement.Pack.Pack,
		RequiredCredits:  quote.MaxCredits,
		AvailableCredits: entitlement.AvailableCredits,
		UpgradeURL:       entitlement.UpgradeURL,
	}
}

func (e Entitlement) Depleted() bool {
	return e.Status == StatusDepleted || e.GraceState == GraceDepleted || (e.AvailableCredits <= 0 && e.ReservedCredits <= 0)
}

func statusForPack(pack string) string {
	if pack == PackTrial {
		return StatusTrialing
	}
	return StatusActive
}

func normalizePackLamports(values map[string]int64) map[string]int64 {
	normalized := map[string]int64{}
	for key, value := range values {
		pack, ok := NormalizePack(key)
		if !ok || pack == PackTrial || value <= 0 {
			continue
		}
		normalized[pack] = value
	}
	return normalized
}

func firstLamportsMap(primary, secondary map[string]int64) map[string]int64 {
	if len(primary) > 0 {
		return primary
	}
	return secondary
}

func (s *Service) lamportsForPack(pack PackDefinition) (int64, int64, error) {
	if override := s.cfg.SolanaPackLamports[pack.Pack]; override > 0 {
		return override, s.cfg.SolanaUSDCentsPerSOL, nil
	}
	if s.cfg.SolanaUSDCentsPerSOL <= 0 {
		return 0, 0, ErrSolPriceNotConfigured
	}
	lamports := (int64(pack.PriceCents)*1_000_000_000 + s.cfg.SolanaUSDCentsPerSOL - 1) / s.cfg.SolanaUSDCentsPerSOL
	return lamports, s.cfg.SolanaUSDCentsPerSOL, nil
}

func (s *Service) setGuildReadOnly(ctx context.Context, guildID, provider, _ string, now time.Time) error {
	account, ok, err := s.repo.GetCreditAccountByGuild(ctx, guildID)
	if err != nil || !ok {
		return err
	}
	readOnlyAt := now
	_, err = s.repo.EnsureCreditAccount(ctx, store.CreditAccount{
		GuildID:                    account.GuildID,
		BillingOwnerUserID:         account.BillingOwnerUserID,
		Status:                     StatusReadOnly,
		PaymentProvider:            firstNonEmpty(provider, account.PaymentProvider),
		ActivePack:                 account.ActivePack,
		RetentionDays:              account.RetentionDays,
		KnowledgeStorageBytesLimit: account.KnowledgeStorageBytesLimit,
		ReadOnlyAt:                 &readOnlyAt,
	})
	return err
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

func (e Entitlement) UsageLine(metric string) string {
	return e.CreditLine()
}

func (e Entitlement) CreditLine() string {
	return FormatCredits(e.AvailableCredits, e.ReservedCredits)
}

func (e Entitlement) SummaryText() string {
	return strings.Join([]string{
		fmt.Sprintf("Pack: %s (%s)", e.Pack.DisplayName, e.Status),
		fmt.Sprintf("Provider: %s", firstNonEmpty(e.PaymentProvider, "unknown")),
		fmt.Sprintf("Credits: %s", e.CreditLine()),
		fmt.Sprintf("Retention: %d days", e.Pack.RetentionDays),
		fmt.Sprintf("Knowledge cap: %s", formatBytes(e.Pack.KnowledgeStorageBytes)),
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

func safeLedgerID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, value)
	if value == "" {
		return "guild"
	}
	if len(value) > 40 {
		return value[:40]
	}
	return value
}
