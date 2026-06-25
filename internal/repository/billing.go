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

func (r *BillingRepository) GetSubscriptionByGuild(ctx context.Context, guildID string) (store.GuildSubscription, bool, error) {
	var subscription store.GuildSubscription
	result := r.db.WithContext(ctx).Where("guild_id = ?", strings.TrimSpace(guildID)).Limit(1).Find(&subscription)
	if result.Error != nil {
		return store.GuildSubscription{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return store.GuildSubscription{}, false, nil
	}
	return subscription, true, nil
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

type CouponRedemptionCounts struct {
	Pending  int
	Consumed int
	Released int
}

func (r *BillingRepository) CreateBillingOrder(ctx context.Context, order store.BillingOrder) (store.BillingOrder, error) {
	now := time.Now().UTC()
	order.OrderID = strings.TrimSpace(order.OrderID)
	order.GuildID = strings.TrimSpace(order.GuildID)
	order.BillingOwnerUserID = strings.TrimSpace(order.BillingOwnerUserID)
	order.SupportEmail = strings.TrimSpace(order.SupportEmail)
	order.Plan = strings.TrimSpace(order.Plan)
	order.Provider = strings.TrimSpace(order.Provider)
	order.CouponID = strings.TrimSpace(order.CouponID)
	order.CouponPrefix = strings.TrimSpace(order.CouponPrefix)
	order.DestinationWallet = strings.TrimSpace(order.DestinationWallet)
	order.Reference = strings.TrimSpace(order.Reference)
	order.Status = strings.TrimSpace(order.Status)
	order.Cluster = strings.TrimSpace(order.Cluster)
	order.ConfirmationThreshold = strings.TrimSpace(order.ConfirmationThreshold)
	if order.OrderID == "" || order.Plan == "" || order.Provider == "" || order.ListLamports <= 0 || order.DueLamports < 0 || order.Reference == "" || order.Status == "" {
		return store.BillingOrder{}, fmt.Errorf("billing order is missing required fields")
	}
	if order.DueLamports > 0 && (order.DestinationWallet == "" || order.Cluster == "" || order.ConfirmationThreshold == "") {
		return store.BillingOrder{}, fmt.Errorf("paid billing order is missing sol payment fields")
	}
	if order.ExpiresAt.IsZero() {
		return store.BillingOrder{}, fmt.Errorf("billing order expiration is required")
	}
	if order.CreatedAt.IsZero() {
		order.CreatedAt = now
	}
	order.UpdatedAt = now
	if err := r.db.WithContext(ctx).Create(&order).Error; err != nil {
		return store.BillingOrder{}, err
	}
	return order, nil
}

func (r *BillingRepository) GetBillingOrder(ctx context.Context, orderID string) (store.BillingOrder, bool, error) {
	var order store.BillingOrder
	err := r.db.WithContext(ctx).Where("order_id = ?", strings.TrimSpace(orderID)).First(&order).Error
	if err == nil {
		return order, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.BillingOrder{}, false, nil
	}
	return store.BillingOrder{}, false, err
}

func (r *BillingRepository) GetBillingOrderForUpdate(ctx context.Context, orderID string) (store.BillingOrder, bool, error) {
	var order store.BillingOrder
	err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("order_id = ?", strings.TrimSpace(orderID)).
		First(&order).Error
	if err == nil {
		return order, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.BillingOrder{}, false, nil
	}
	return store.BillingOrder{}, false, err
}

func (r *BillingRepository) UpdateBillingOrder(ctx context.Context, orderID string, updates map[string]any) error {
	if strings.TrimSpace(orderID) == "" {
		return fmt.Errorf("order_id is required")
	}
	if updates == nil {
		updates = map[string]any{}
	}
	updates["updated_at"] = time.Now().UTC()
	return r.db.WithContext(ctx).Model(&store.BillingOrder{}).
		Where("order_id = ?", strings.TrimSpace(orderID)).
		Updates(updates).Error
}

func (r *BillingRepository) CreateBillingCoupon(ctx context.Context, coupon store.BillingCoupon) (store.BillingCoupon, error) {
	now := time.Now().UTC()
	coupon.CouponID = strings.TrimSpace(coupon.CouponID)
	coupon.CodeHash = strings.TrimSpace(coupon.CodeHash)
	coupon.CodePrefix = strings.TrimSpace(coupon.CodePrefix)
	coupon.Plan = strings.TrimSpace(coupon.Plan)
	coupon.Status = strings.TrimSpace(coupon.Status)
	coupon.OwnerNote = strings.TrimSpace(coupon.OwnerNote)
	coupon.CreatedByUserID = strings.TrimSpace(coupon.CreatedByUserID)
	if coupon.CouponID == "" || coupon.CodeHash == "" || coupon.CodePrefix == "" || coupon.Plan == "" || coupon.DiscountLamports <= 0 || coupon.Status == "" {
		return store.BillingCoupon{}, fmt.Errorf("billing coupon is missing required fields")
	}
	if coupon.MaxRedemptions < 0 {
		return store.BillingCoupon{}, fmt.Errorf("max redemptions cannot be negative")
	}
	if coupon.CreatedAt.IsZero() {
		coupon.CreatedAt = now
	}
	coupon.UpdatedAt = now
	if err := r.db.WithContext(ctx).Create(&coupon).Error; err != nil {
		return store.BillingCoupon{}, err
	}
	return coupon, nil
}

func (r *BillingRepository) ListBillingCoupons(ctx context.Context) ([]store.BillingCoupon, error) {
	var coupons []store.BillingCoupon
	err := r.db.WithContext(ctx).
		Order("created_at DESC, id DESC").
		Find(&coupons).Error
	return coupons, err
}

func (r *BillingRepository) GetBillingCouponByCodeHashForUpdate(ctx context.Context, codeHash string) (store.BillingCoupon, bool, error) {
	var coupon store.BillingCoupon
	err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("code_hash = ?", strings.TrimSpace(codeHash)).
		First(&coupon).Error
	if err == nil {
		return coupon, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.BillingCoupon{}, false, nil
	}
	return store.BillingCoupon{}, false, err
}

func (r *BillingRepository) GetBillingCouponByIDForUpdate(ctx context.Context, couponID string) (store.BillingCoupon, bool, error) {
	var coupon store.BillingCoupon
	err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("coupon_id = ?", strings.TrimSpace(couponID)).
		First(&coupon).Error
	if err == nil {
		return coupon, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.BillingCoupon{}, false, nil
	}
	return store.BillingCoupon{}, false, err
}

func (r *BillingRepository) FindBillingCouponsByPrefixForUpdate(ctx context.Context, prefix string) ([]store.BillingCoupon, error) {
	var coupons []store.BillingCoupon
	err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("code_prefix = ? OR coupon_id = ?", strings.TrimSpace(prefix), strings.TrimSpace(prefix)).
		Order("created_at DESC, id DESC").
		Find(&coupons).Error
	return coupons, err
}

func (r *BillingRepository) UpdateBillingCoupon(ctx context.Context, couponID string, updates map[string]any) error {
	if strings.TrimSpace(couponID) == "" {
		return fmt.Errorf("coupon_id is required")
	}
	if updates == nil {
		updates = map[string]any{}
	}
	updates["updated_at"] = time.Now().UTC()
	return r.db.WithContext(ctx).Model(&store.BillingCoupon{}).
		Where("coupon_id = ?", strings.TrimSpace(couponID)).
		Updates(updates).Error
}

func (r *BillingRepository) CreateCouponRedemption(ctx context.Context, redemption store.BillingCouponRedemption) (store.BillingCouponRedemption, error) {
	now := time.Now().UTC()
	redemption.RedemptionID = strings.TrimSpace(redemption.RedemptionID)
	redemption.CouponID = strings.TrimSpace(redemption.CouponID)
	redemption.OrderID = strings.TrimSpace(redemption.OrderID)
	redemption.GuildID = strings.TrimSpace(redemption.GuildID)
	redemption.BillingOwnerUserID = strings.TrimSpace(redemption.BillingOwnerUserID)
	redemption.Plan = strings.TrimSpace(redemption.Plan)
	redemption.Status = strings.TrimSpace(redemption.Status)
	if redemption.RedemptionID == "" || redemption.CouponID == "" || redemption.OrderID == "" || redemption.Plan == "" || redemption.ListLamports <= 0 || redemption.DiscountLamports <= 0 || redemption.DueLamports < 0 || redemption.Status == "" || redemption.ExpiresAt.IsZero() {
		return store.BillingCouponRedemption{}, fmt.Errorf("billing coupon redemption is missing required fields")
	}
	if redemption.CreatedAt.IsZero() {
		redemption.CreatedAt = now
	}
	redemption.UpdatedAt = now
	if err := r.db.WithContext(ctx).Create(&redemption).Error; err != nil {
		return store.BillingCouponRedemption{}, err
	}
	return redemption, nil
}

func (r *BillingRepository) GetCouponRedemptionByOrderForUpdate(ctx context.Context, orderID string) (store.BillingCouponRedemption, bool, error) {
	var redemption store.BillingCouponRedemption
	err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("order_id = ?", strings.TrimSpace(orderID)).
		First(&redemption).Error
	if err == nil {
		return redemption, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.BillingCouponRedemption{}, false, nil
	}
	return store.BillingCouponRedemption{}, false, err
}

func (r *BillingRepository) UpdateCouponRedemption(ctx context.Context, redemptionID string, updates map[string]any) error {
	if strings.TrimSpace(redemptionID) == "" {
		return fmt.Errorf("redemption_id is required")
	}
	if updates == nil {
		updates = map[string]any{}
	}
	updates["updated_at"] = time.Now().UTC()
	return r.db.WithContext(ctx).Model(&store.BillingCouponRedemption{}).
		Where("redemption_id = ?", strings.TrimSpace(redemptionID)).
		Updates(updates).Error
}

func (r *BillingRepository) CouponRedemptionCounts(ctx context.Context, couponID string) (CouponRedemptionCounts, error) {
	var rows []struct {
		Status string
		Count  int
	}
	err := r.db.WithContext(ctx).
		Model(&store.BillingCouponRedemption{}).
		Select("status, COUNT(*) as count").
		Where("coupon_id = ?", strings.TrimSpace(couponID)).
		Group("status").
		Scan(&rows).Error
	if err != nil {
		return CouponRedemptionCounts{}, err
	}
	var counts CouponRedemptionCounts
	for _, row := range rows {
		switch row.Status {
		case "pending":
			counts.Pending = row.Count
		case "consumed":
			counts.Consumed = row.Count
		case "released":
			counts.Released = row.Count
		}
	}
	return counts, nil
}

func (r *BillingRepository) CouponRedemptionCountsForCoupons(ctx context.Context, couponIDs []string) (map[string]CouponRedemptionCounts, error) {
	normalized := make([]string, 0, len(couponIDs))
	for _, couponID := range couponIDs {
		if couponID = strings.TrimSpace(couponID); couponID != "" {
			normalized = append(normalized, couponID)
		}
	}
	if len(normalized) == 0 {
		return map[string]CouponRedemptionCounts{}, nil
	}
	var rows []struct {
		CouponID string
		Status   string
		Count    int
	}
	err := r.db.WithContext(ctx).
		Model(&store.BillingCouponRedemption{}).
		Select("coupon_id, status, COUNT(*) as count").
		Where("coupon_id IN ?", normalized).
		Group("coupon_id, status").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make(map[string]CouponRedemptionCounts, len(normalized))
	for _, row := range rows {
		counts := result[row.CouponID]
		switch row.Status {
		case "pending":
			counts.Pending = row.Count
		case "consumed":
			counts.Consumed = row.Count
		case "released":
			counts.Released = row.Count
		}
		result[row.CouponID] = counts
	}
	return result, nil
}

func (r *BillingRepository) RecordSolPaymentTransaction(ctx context.Context, transaction store.SolPaymentTransaction) (bool, error) {
	now := time.Now().UTC()
	transaction.Signature = strings.TrimSpace(transaction.Signature)
	transaction.OrderID = strings.TrimSpace(transaction.OrderID)
	transaction.GuildID = strings.TrimSpace(transaction.GuildID)
	transaction.PayerWallet = strings.TrimSpace(transaction.PayerWallet)
	transaction.DestinationWallet = strings.TrimSpace(transaction.DestinationWallet)
	transaction.Reference = strings.TrimSpace(transaction.Reference)
	transaction.ConfirmationStatus = strings.TrimSpace(transaction.ConfirmationStatus)
	transaction.Status = strings.TrimSpace(transaction.Status)
	transaction.ErrorMessage = strings.TrimSpace(transaction.ErrorMessage)
	if transaction.Signature == "" || transaction.OrderID == "" || transaction.Status == "" {
		return false, fmt.Errorf("sol payment transaction is missing required fields")
	}
	if transaction.CreatedAt.IsZero() {
		transaction.CreatedAt = now
	}
	transaction.UpdatedAt = now
	if strings.TrimSpace(transaction.RawPayload) == "" {
		transaction.RawPayload = "{}"
	}
	err := r.db.WithContext(ctx).Create(&transaction).Error
	if err == nil {
		return true, nil
	}
	if isBillingUniqueConstraintError(err) {
		return false, nil
	}
	return false, err
}

func (r *BillingRepository) CreateActivationAPIKey(ctx context.Context, key store.ActivationAPIKey) (store.ActivationAPIKey, error) {
	now := time.Now().UTC()
	key.KeyID = strings.TrimSpace(key.KeyID)
	key.KeyHash = strings.TrimSpace(key.KeyHash)
	key.KeyPrefix = strings.TrimSpace(key.KeyPrefix)
	key.BillingOrderID = strings.TrimSpace(key.BillingOrderID)
	key.GuildID = strings.TrimSpace(key.GuildID)
	key.Plan = strings.TrimSpace(key.Plan)
	key.Status = strings.TrimSpace(key.Status)
	if key.KeyID == "" || key.KeyHash == "" || key.KeyPrefix == "" || key.BillingOrderID == "" || key.Plan == "" || key.Status == "" || key.ExpiresAt.IsZero() {
		return store.ActivationAPIKey{}, fmt.Errorf("activation api key is missing required fields")
	}
	if key.CreatedAt.IsZero() {
		key.CreatedAt = now
	}
	key.UpdatedAt = now
	if err := r.db.WithContext(ctx).Create(&key).Error; err != nil {
		return store.ActivationAPIKey{}, err
	}
	return key, nil
}

func (r *BillingRepository) GetActivationAPIKeyByPaymentOrder(ctx context.Context, orderID string) (store.ActivationAPIKey, bool, error) {
	var key store.ActivationAPIKey
	err := r.db.WithContext(ctx).Where("billing_order_id = ?", strings.TrimSpace(orderID)).First(&key).Error
	if err == nil {
		return key, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.ActivationAPIKey{}, false, nil
	}
	return store.ActivationAPIKey{}, false, err
}

func (r *BillingRepository) GetActivationAPIKeyByPaymentOrderForUpdate(ctx context.Context, orderID string) (store.ActivationAPIKey, bool, error) {
	var key store.ActivationAPIKey
	err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("billing_order_id = ?", strings.TrimSpace(orderID)).
		First(&key).Error
	if err == nil {
		return key, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.ActivationAPIKey{}, false, nil
	}
	return store.ActivationAPIKey{}, false, err
}

func (r *BillingRepository) GetActivationAPIKeyByHashForUpdate(ctx context.Context, keyHash string) (store.ActivationAPIKey, bool, error) {
	var key store.ActivationAPIKey
	err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("key_hash = ?", strings.TrimSpace(keyHash)).
		First(&key).Error
	if err == nil {
		return key, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.ActivationAPIKey{}, false, nil
	}
	return store.ActivationAPIKey{}, false, err
}

func (r *BillingRepository) UpdateActivationAPIKey(ctx context.Context, keyID string, updates map[string]any) error {
	if strings.TrimSpace(keyID) == "" {
		return fmt.Errorf("key_id is required")
	}
	if updates == nil {
		updates = map[string]any{}
	}
	updates["updated_at"] = time.Now().UTC()
	return r.db.WithContext(ctx).Model(&store.ActivationAPIKey{}).
		Where("key_id = ?", strings.TrimSpace(keyID)).
		Updates(updates).Error
}

func (r *BillingRepository) UsageTotals(ctx context.Context, guildID string, periodStart, periodEnd time.Time) (BillingUsageTotals, error) {
	period, ok, err := findUsagePeriodByWindow(r.db.WithContext(ctx), strings.TrimSpace(guildID), periodStart, periodEnd, false)
	if err != nil {
		return BillingUsageTotals{}, err
	}
	if !ok {
		return BillingUsageTotals{}, nil
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
	periodStart := subscription.CurrentPeriodStart.UTC()
	periodEnd := subscription.CurrentPeriodEnd.UTC()
	period, ok, err := findUsagePeriodByWindow(tx, subscription.GuildID, periodStart, periodEnd, true)
	if err != nil {
		return store.UsagePeriod{}, err
	}
	if ok {
		return period, nil
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

func findUsagePeriodByWindow(tx *gorm.DB, guildID string, periodStart, periodEnd time.Time, forUpdate bool) (store.UsagePeriod, bool, error) {
	var period store.UsagePeriod
	query := tx.
		Where("guild_id = ? AND period_start = ? AND period_end = ?", strings.TrimSpace(guildID), periodStart.UTC(), periodEnd.UTC()).
		Limit(1)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	result := query.Find(&period)
	if result.Error != nil {
		return store.UsagePeriod{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return store.UsagePeriod{}, false, nil
	}
	return period, true, nil
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

func IsUniqueConstraintError(err error) bool {
	return isBillingUniqueConstraintError(err)
}
