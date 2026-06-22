package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type BillingRepository struct {
	db *gorm.DB
}

type BillingUsageTotals struct {
	AIResponsesConsumed           int64
	AIResponsesReserved           int64
	WebSearchesConsumed           int64
	WebSearchesReserved           int64
	KnowledgeStorageBytesConsumed int64
	KnowledgeStorageBytesReserved int64
	ScheduledRunsConsumed         int64
	ScheduledRunsReserved         int64
	MusicMinutesConsumed          int64
	MusicMinutesReserved          int64
}

func NewBillingRepository(db *gorm.DB) *BillingRepository {
	return &BillingRepository{db: db}
}

func (r *BillingRepository) EnsureCustomerAccount(ctx context.Context, account store.CustomerAccount) (store.CustomerAccount, error) {
	now := time.Now().UTC()
	account.GuildID = strings.TrimSpace(account.GuildID)
	account.BillingOwnerUserID = strings.TrimSpace(account.BillingOwnerUserID)
	account.Email = strings.TrimSpace(account.Email)
	account.TaxCountry = strings.ToUpper(strings.TrimSpace(account.TaxCountry))
	account.SupportContact = strings.TrimSpace(account.SupportContact)
	account.StripeCustomerID = strings.TrimSpace(account.StripeCustomerID)
	account.CreatedAt = now
	account.UpdatedAt = now
	if account.GuildID == "" {
		return store.CustomerAccount{}, fmt.Errorf("guild_id is required")
	}

	var saved store.CustomerAccount
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing store.CustomerAccount
		err := tx.Where("guild_id = ?", account.GuildID).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&account).Error; err != nil {
				return err
			}
			saved = account
			return nil
		}
		updates := map[string]any{"updated_at": now}
		if account.BillingOwnerUserID != "" {
			updates["billing_owner_user_id"] = account.BillingOwnerUserID
		}
		if account.Email != "" {
			updates["email"] = account.Email
		}
		if account.TaxCountry != "" {
			updates["tax_country"] = account.TaxCountry
		}
		if account.SupportContact != "" {
			updates["support_contact"] = account.SupportContact
		}
		if account.StripeCustomerID != "" {
			updates["stripe_customer_id"] = account.StripeCustomerID
		}
		if err := tx.Model(&existing).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", existing.ID).First(&saved).Error
	})
	return saved, err
}

func (r *BillingRepository) GetCustomerAccountByGuild(ctx context.Context, guildID string) (store.CustomerAccount, bool, error) {
	var account store.CustomerAccount
	err := r.db.WithContext(ctx).Where("guild_id = ?", strings.TrimSpace(guildID)).First(&account).Error
	if err == nil {
		return account, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.CustomerAccount{}, false, nil
	}
	return store.CustomerAccount{}, false, err
}

func (r *BillingRepository) SetStripeCustomerID(ctx context.Context, guildID, stripeCustomerID string) error {
	guildID = strings.TrimSpace(guildID)
	stripeCustomerID = strings.TrimSpace(stripeCustomerID)
	if guildID == "" || stripeCustomerID == "" {
		return fmt.Errorf("guild_id and stripe_customer_id are required")
	}
	return r.db.WithContext(ctx).Model(&store.CustomerAccount{}).
		Where("guild_id = ?", guildID).
		Updates(map[string]any{
			"stripe_customer_id": stripeCustomerID,
			"updated_at":         time.Now().UTC(),
		}).Error
}

func (r *BillingRepository) GetSubscriptionByGuild(ctx context.Context, guildID string) (store.GuildSubscription, bool, error) {
	var subscription store.GuildSubscription
	err := r.db.WithContext(ctx).Where("guild_id = ?", strings.TrimSpace(guildID)).First(&subscription).Error
	if err == nil {
		return subscription, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.GuildSubscription{}, false, nil
	}
	return store.GuildSubscription{}, false, err
}

func (r *BillingRepository) GetSubscriptionByExternalSubscriptionID(ctx context.Context, externalSubscriptionID string) (store.GuildSubscription, bool, error) {
	var subscription store.GuildSubscription
	err := r.db.WithContext(ctx).Where("external_subscription_id = ?", strings.TrimSpace(externalSubscriptionID)).First(&subscription).Error
	if err == nil {
		return subscription, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.GuildSubscription{}, false, nil
	}
	return store.GuildSubscription{}, false, err
}

func (r *BillingRepository) WithTransaction(ctx context.Context, fn func(*BillingRepository) error) error {
	if fn == nil {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&BillingRepository{db: tx})
	})
}

func (r *BillingRepository) UpsertSubscriptionWithSnapshot(ctx context.Context, subscription store.GuildSubscription, snapshot store.EntitlementSnapshot) (store.GuildSubscription, error) {
	now := time.Now().UTC()
	subscription.GuildID = strings.TrimSpace(subscription.GuildID)
	subscription.Plan = strings.TrimSpace(subscription.Plan)
	subscription.Status = strings.TrimSpace(subscription.Status)
	subscription.GraceState = strings.TrimSpace(subscription.GraceState)
	subscription.PaymentProvider = strings.TrimSpace(subscription.PaymentProvider)
	subscription.ExternalSubscriptionID = strings.TrimSpace(subscription.ExternalSubscriptionID)
	subscription.ExternalEntitlementID = strings.TrimSpace(subscription.ExternalEntitlementID)
	subscription.BillingOwnerUserID = strings.TrimSpace(subscription.BillingOwnerUserID)
	if subscription.GuildID == "" || subscription.Plan == "" || subscription.Status == "" || subscription.GraceState == "" {
		return store.GuildSubscription{}, fmt.Errorf("guild_id, plan, status, and grace_state are required")
	}
	if subscription.CurrentPeriodStart.IsZero() || subscription.CurrentPeriodEnd.IsZero() || !subscription.CurrentPeriodEnd.After(subscription.CurrentPeriodStart) {
		return store.GuildSubscription{}, fmt.Errorf("subscription period is required")
	}
	subscription.CreatedAt = now
	subscription.UpdatedAt = now

	var saved store.GuildSubscription
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing store.GuildSubscription
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guild_id = ?", subscription.GuildID).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&subscription).Error; err != nil {
				return err
			}
			saved = subscription
		} else {
			updates := map[string]any{
				"customer_account_id":      subscription.CustomerAccountID,
				"plan":                     subscription.Plan,
				"status":                   subscription.Status,
				"grace_state":              subscription.GraceState,
				"payment_provider":         subscription.PaymentProvider,
				"external_subscription_id": subscription.ExternalSubscriptionID,
				"external_entitlement_id":  subscription.ExternalEntitlementID,
				"billing_owner_user_id":    subscription.BillingOwnerUserID,
				"current_period_start":     subscription.CurrentPeriodStart,
				"current_period_end":       subscription.CurrentPeriodEnd,
				"trial_ends_at":            subscription.TrialEndsAt,
				"cancel_at_period_end":     subscription.CancelAtPeriodEnd,
				"updated_at":               now,
			}
			if err := tx.Model(&existing).Updates(updates).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", existing.ID).First(&saved).Error; err != nil {
				return err
			}
		}

		if err := tx.Model(&store.EntitlementSnapshot{}).
			Where("guild_id = ? AND expires_at IS NULL", saved.GuildID).
			Update("expires_at", now).Error; err != nil {
			return err
		}
		snapshot.GuildID = saved.GuildID
		snapshot.SubscriptionID = saved.ID
		if snapshot.CreatedAt.IsZero() {
			snapshot.CreatedAt = now
		}
		return tx.Create(&snapshot).Error
	})
	return saved, err
}

func (r *BillingRepository) LatestEntitlementSnapshot(ctx context.Context, guildID string) (store.EntitlementSnapshot, bool, error) {
	var snapshot store.EntitlementSnapshot
	err := r.db.WithContext(ctx).
		Where("guild_id = ? AND expires_at IS NULL", strings.TrimSpace(guildID)).
		Order("created_at DESC, id DESC").
		First(&snapshot).Error
	if err == nil {
		return snapshot, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.EntitlementSnapshot{}, false, nil
	}
	return store.EntitlementSnapshot{}, false, err
}

func (r *BillingRepository) RecordInvoicePaymentEvent(ctx context.Context, event store.InvoicePaymentEvent) (bool, error) {
	event.Provider = strings.TrimSpace(event.Provider)
	event.ExternalID = strings.TrimSpace(event.ExternalID)
	event.GuildID = strings.TrimSpace(event.GuildID)
	event.Currency = strings.ToLower(strings.TrimSpace(event.Currency))
	event.Status = strings.TrimSpace(event.Status)
	event.IdempotencyKey = strings.TrimSpace(event.IdempotencyKey)
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if event.Provider == "" || event.ExternalID == "" || event.Status == "" || event.IdempotencyKey == "" {
		return false, fmt.Errorf("provider, external_id, status, and idempotency_key are required")
	}

	err := r.db.WithContext(ctx).Create(&event).Error
	if err == nil {
		return true, nil
	}
	if isBillingUniqueConstraintError(err) {
		return false, nil
	}
	return false, err
}

func (r *BillingRepository) UsageTotals(ctx context.Context, guildID string, periodStart, periodEnd time.Time) (BillingUsageTotals, error) {
	var period store.UsagePeriod
	err := r.db.WithContext(ctx).
		Where("guild_id = ? AND period_start = ? AND period_end = ?", strings.TrimSpace(guildID), periodStart, periodEnd).
		First(&period).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return BillingUsageTotals{}, nil
	}
	if err != nil {
		return BillingUsageTotals{}, err
	}
	return totalsFromPeriod(period), nil
}

func (r *BillingRepository) BeginUsageReservation(ctx context.Context, subscription store.GuildSubscription, metric string, units int64, includedLimit int64, now time.Time) (store.UsageReservation, BillingUsageTotals, bool, error) {
	return r.beginUsageReservation(ctx, subscription, metric, units, includedLimit, nil, now)
}

func (r *BillingRepository) BeginCurrentUsageReservation(ctx context.Context, subscription store.GuildSubscription, metric string, units int64, currentUsed int64, includedLimit int64, now time.Time) (store.UsageReservation, BillingUsageTotals, bool, error) {
	if currentUsed < 0 {
		currentUsed = 0
	}
	return r.beginUsageReservation(ctx, subscription, metric, units, includedLimit, &currentUsed, now)
}

func (r *BillingRepository) SyncCurrentUsage(ctx context.Context, subscription store.GuildSubscription, metric string, currentUsed int64, now time.Time) error {
	if currentUsed < 0 {
		currentUsed = 0
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	column := consumedColumn(strings.TrimSpace(metric))
	if column == "" {
		return fmt.Errorf("unsupported usage metric")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		period, err := ensureUsagePeriodTx(tx, subscription, now)
		if err != nil {
			return err
		}
		return tx.Model(&store.UsagePeriod{}).
			Where("id = ?", period.ID).
			Updates(map[string]any{
				column:       currentUsed,
				"updated_at": now,
			}).Error
	})
}

func (r *BillingRepository) beginUsageReservation(ctx context.Context, subscription store.GuildSubscription, metric string, units int64, includedLimit int64, currentUsed *int64, now time.Time) (store.UsageReservation, BillingUsageTotals, bool, error) {
	metric = strings.TrimSpace(metric)
	if units <= 0 {
		return store.UsageReservation{}, BillingUsageTotals{}, false, fmt.Errorf("usage units must be positive")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	reservationID := fmt.Sprintf("%d-%s-%d", now.UnixNano(), subscription.GuildID, units)
	var reservation store.UsageReservation
	var totals BillingUsageTotals
	var denied bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		period, err := ensureUsagePeriodTx(tx, subscription, now)
		if err != nil {
			return err
		}
		if currentUsed != nil {
			if err := tx.Model(&store.UsagePeriod{}).
				Where("id = ?", period.ID).
				Updates(map[string]any{
					consumedColumn(metric): *currentUsed,
					"updated_at":           now,
				}).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", period.ID).First(&period).Error; err != nil {
				return err
			}
		}
		totals = totalsFromPeriod(period)
		used := metricConsumed(totals, metric) + metricReserved(totals, metric)
		if used+units > includedLimit {
			denied = true
			return nil
		}
		if err := incrementReserved(tx, period.ID, metric, units); err != nil {
			return err
		}
		reservation = store.UsageReservation{
			ReservationID:  reservationID,
			GuildID:        subscription.GuildID,
			SubscriptionID: subscription.ID,
			UsagePeriodID:  period.ID,
			Metric:         metric,
			Units:          units,
			Status:         "pending",
			ExpiresAt:      now.Add(30 * time.Minute),
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := tx.Create(&reservation).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", period.ID).First(&period).Error; err != nil {
			return err
		}
		totals = totalsFromPeriod(period)
		return nil
	})
	return reservation, totals, denied, err
}

func (r *BillingRepository) CommitUsageReservation(ctx context.Context, reservationID string) error {
	return r.finishUsageReservation(ctx, reservationID, true)
}

func (r *BillingRepository) ReleaseUsageReservation(ctx context.Context, reservationID string) error {
	return r.finishUsageReservation(ctx, reservationID, false)
}

func (r *BillingRepository) finishUsageReservation(ctx context.Context, reservationID string, consume bool) error {
	reservationID = strings.TrimSpace(reservationID)
	if reservationID == "" {
		return nil
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reservation store.UsageReservation
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("reservation_id = ?", reservationID).
			First(&reservation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if reservation.Status != "pending" {
			return nil
		}
		if err := decrementReserved(tx, reservation.UsagePeriodID, reservation.Metric, reservation.Units); err != nil {
			return err
		}
		status := "released"
		if consume {
			status = "consumed"
			if err := incrementConsumed(tx, reservation.UsagePeriodID, reservation.Metric, reservation.Units); err != nil {
				return err
			}
		}
		return tx.Model(&reservation).Updates(map[string]any{
			"status":     status,
			"updated_at": now,
		}).Error
	})
}

func (r *BillingRepository) RecordCostLedgerEvent(ctx context.Context, event store.CostLedgerEvent) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	event.GuildID = strings.TrimSpace(event.GuildID)
	event.RequestID = strings.TrimSpace(event.RequestID)
	event.Source = strings.TrimSpace(event.Source)
	event.Operation = strings.TrimSpace(event.Operation)
	event.Command = strings.TrimSpace(event.Command)
	event.Provider = strings.TrimSpace(event.Provider)
	event.Model = strings.TrimSpace(event.Model)
	event.ErrorCode = strings.TrimSpace(event.ErrorCode)
	if event.Source == "" || event.Operation == "" {
		return fmt.Errorf("cost ledger source and operation are required")
	}
	return r.db.WithContext(ctx).Create(&event).Error
}

func ensureUsagePeriodTx(tx *gorm.DB, subscription store.GuildSubscription, now time.Time) (store.UsagePeriod, error) {
	var period store.UsagePeriod
	periodStart := subscription.CurrentPeriodStart.UTC()
	periodEnd := subscription.CurrentPeriodEnd.UTC()
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("guild_id = ? AND period_start = ? AND period_end = ?", subscription.GuildID, periodStart, periodEnd).
		First(&period).Error
	if err == nil {
		return period, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return store.UsagePeriod{}, err
	}
	period = store.UsagePeriod{
		GuildID:        subscription.GuildID,
		SubscriptionID: subscription.ID,
		Plan:           subscription.Plan,
		PeriodStart:    periodStart,
		PeriodEnd:      periodEnd,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := tx.Create(&period).Error; err != nil {
		return store.UsagePeriod{}, err
	}
	return period, nil
}

func incrementReserved(tx *gorm.DB, periodID uint, metric string, units int64) error {
	return incrementUsageColumn(tx, periodID, reservedColumn(metric), units)
}

func decrementReserved(tx *gorm.DB, periodID uint, metric string, units int64) error {
	return incrementUsageColumn(tx, periodID, reservedColumn(metric), -units)
}

func incrementConsumed(tx *gorm.DB, periodID uint, metric string, units int64) error {
	return incrementUsageColumn(tx, periodID, consumedColumn(metric), units)
}

func incrementUsageColumn(tx *gorm.DB, periodID uint, column string, units int64) error {
	if column == "" {
		return fmt.Errorf("unsupported usage metric")
	}
	return tx.Model(&store.UsagePeriod{}).
		Where("id = ?", periodID).
		UpdateColumn(column, gorm.Expr(column+" + ?", units)).Error
}

func consumedColumn(metric string) string {
	switch metric {
	case "ai_response":
		return "ai_responses_consumed"
	case "web_search":
		return "web_searches_consumed"
	case "knowledge_storage_byte":
		return "knowledge_storage_bytes_consumed"
	case "scheduled_run":
		return "scheduled_runs_consumed"
	case "music_minute":
		return "music_playback_minutes_consumed"
	default:
		return ""
	}
}

func reservedColumn(metric string) string {
	switch metric {
	case "ai_response":
		return "ai_responses_reserved"
	case "web_search":
		return "web_searches_reserved"
	case "knowledge_storage_byte":
		return "knowledge_storage_bytes_reserved"
	case "scheduled_run":
		return "scheduled_runs_reserved"
	case "music_minute":
		return "music_playback_minutes_reserved"
	default:
		return ""
	}
}

func totalsFromPeriod(period store.UsagePeriod) BillingUsageTotals {
	return BillingUsageTotals{
		AIResponsesConsumed:           int64(period.AIResponsesConsumed),
		AIResponsesReserved:           int64(period.AIResponsesReserved),
		WebSearchesConsumed:           int64(period.WebSearchesConsumed),
		WebSearchesReserved:           int64(period.WebSearchesReserved),
		KnowledgeStorageBytesConsumed: period.KnowledgeStorageBytesConsumed,
		KnowledgeStorageBytesReserved: period.KnowledgeStorageBytesReserved,
		ScheduledRunsConsumed:         int64(period.ScheduledRunsConsumed),
		ScheduledRunsReserved:         int64(period.ScheduledRunsReserved),
		MusicMinutesConsumed:          int64(period.MusicPlaybackMinutesConsumed),
		MusicMinutesReserved:          int64(period.MusicPlaybackMinutesReserved),
	}
}

func metricConsumed(totals BillingUsageTotals, metric string) int64 {
	switch metric {
	case "ai_response":
		return totals.AIResponsesConsumed
	case "web_search":
		return totals.WebSearchesConsumed
	case "knowledge_storage_byte":
		return totals.KnowledgeStorageBytesConsumed
	case "scheduled_run":
		return totals.ScheduledRunsConsumed
	case "music_minute":
		return totals.MusicMinutesConsumed
	default:
		return 0
	}
}

func metricReserved(totals BillingUsageTotals, metric string) int64 {
	switch metric {
	case "ai_response":
		return totals.AIResponsesReserved
	case "web_search":
		return totals.WebSearchesReserved
	case "knowledge_storage_byte":
		return totals.KnowledgeStorageBytesReserved
	case "scheduled_run":
		return totals.ScheduledRunsReserved
	case "music_minute":
		return totals.MusicMinutesReserved
	default:
		return 0
	}
}

func isBillingUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "duplicate key")
}
