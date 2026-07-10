package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CreditReservationRequest struct {
	ReservationID   string
	GuildID         string
	Action          string
	RequestID       string
	ExpectedCredits int64
	MaxCredits      int64
	ExpiresAt       time.Time
	MetadataJSON    string
}

type CreditGrantRequest struct {
	GrantID      string
	GuildID      string
	Source       string
	SourceID     string
	Pack         string
	Credits      int64
	ExpiresAt    *time.Time
	MetadataJSON string
}

type CreditCommitRequest struct {
	ReservationID string
	FinalCredits  int64
	MetadataJSON  string
}

type creditAllocation struct {
	GrantID string `json:"grant_id"`
	Credits int64  `json:"credits"`
}

type creditReservationMetadata struct {
	Metadata    json.RawMessage    `json:"metadata,omitempty"`
	Allocations []creditAllocation `json:"allocations"`
	Release     map[string]string  `json:"release,omitempty"`
	Commit      map[string]any     `json:"commit,omitempty"`
}

func (r *BillingRepository) GetCreditAccountByGuild(ctx context.Context, guildID string) (store.CreditAccount, bool, error) {
	var account store.CreditAccount
	err := r.db.WithContext(ctx).Where("guild_id = ?", strings.TrimSpace(guildID)).First(&account).Error
	if err == nil {
		return account, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.CreditAccount{}, false, nil
	}
	return store.CreditAccount{}, false, err
}

func (r *BillingRepository) EnsureCreditAccount(ctx context.Context, account store.CreditAccount) (store.CreditAccount, error) {
	now := time.Now().UTC()
	if !account.CreatedAt.IsZero() {
		now = account.CreatedAt.UTC()
	}
	account.GuildID = strings.TrimSpace(account.GuildID)
	account.BillingOwnerUserID = strings.TrimSpace(account.BillingOwnerUserID)
	account.Status = strings.TrimSpace(account.Status)
	account.PaymentProvider = strings.TrimSpace(account.PaymentProvider)
	account.ActivePack = strings.TrimSpace(account.ActivePack)
	account.SupportState = strings.TrimSpace(account.SupportState)
	account.ExportState = strings.TrimSpace(account.ExportState)
	if account.GuildID == "" || account.Status == "" {
		return store.CreditAccount{}, fmt.Errorf("guild_id and status are required")
	}
	if account.CreatedAt.IsZero() {
		account.CreatedAt = now
	}
	account.UpdatedAt = now

	var saved store.CreditAccount
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing store.CreditAccount
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guild_id = ?", account.GuildID).First(&existing).Error
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
		updates := map[string]any{
			"status":                        firstCreditText(account.Status, existing.Status),
			"payment_provider":              firstCreditText(account.PaymentProvider, existing.PaymentProvider),
			"active_pack":                   firstCreditText(account.ActivePack, existing.ActivePack),
			"retention_days":                nonZeroInt(account.RetentionDays, existing.RetentionDays),
			"knowledge_storage_bytes_limit": nonZeroInt64(account.KnowledgeStorageBytesLimit, existing.KnowledgeStorageBytesLimit),
			"storage_rent_grace_until":      account.StorageRentGraceUntil,
			"support_state":                 firstCreditText(account.SupportState, existing.SupportState),
			"export_state":                  firstCreditText(account.ExportState, existing.ExportState),
			"read_only_at":                  account.ReadOnlyAt,
			"suspended_at":                  account.SuspendedAt,
			"updated_at":                    now,
		}
		if account.BillingOwnerUserID != "" {
			updates["billing_owner_user_id"] = account.BillingOwnerUserID
		}
		if err := tx.Model(&existing).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", existing.ID).First(&saved).Error
	})
	return saved, err
}

func (r *BillingRepository) GrantCredits(ctx context.Context, request CreditGrantRequest) (store.CreditGrant, store.CreditAccount, bool, error) {
	now := time.Now().UTC()
	request.GrantID = strings.TrimSpace(request.GrantID)
	request.GuildID = strings.TrimSpace(request.GuildID)
	request.Source = strings.TrimSpace(request.Source)
	request.SourceID = strings.TrimSpace(request.SourceID)
	request.Pack = strings.TrimSpace(request.Pack)
	request.MetadataJSON = normalizedJSON(request.MetadataJSON)
	if request.GrantID == "" || request.GuildID == "" || request.Source == "" || request.SourceID == "" || request.Credits <= 0 {
		return store.CreditGrant{}, store.CreditAccount{}, false, fmt.Errorf("credit grant is missing required fields")
	}

	var grant store.CreditGrant
	var account store.CreditAccount
	inserted := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existingGrant store.CreditGrant
		err := tx.Where("guild_id = ? AND source = ? AND source_id = ?", request.GuildID, request.Source, request.SourceID).First(&existingGrant).Error
		if err == nil {
			grant = existingGrant
			return tx.Where("id = ?", existingGrant.AccountID).First(&account).Error
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guild_id = ?", request.GuildID).First(&account).Error; err != nil {
			return err
		}
		grant = store.CreditGrant{
			GrantID:          request.GrantID,
			GuildID:          request.GuildID,
			AccountID:        account.ID,
			Source:           request.Source,
			SourceID:         request.SourceID,
			Pack:             request.Pack,
			CreditsGranted:   request.Credits,
			CreditsRemaining: request.Credits,
			ExpiresAt:        request.ExpiresAt,
			MetadataJSON:     request.MetadataJSON,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := tx.Create(&grant).Error; err != nil {
			if isBillingUniqueConstraintError(err) {
				return tx.Where("guild_id = ? AND source = ? AND source_id = ?", request.GuildID, request.Source, request.SourceID).First(&grant).Error
			}
			return err
		}
		if err := tx.Model(&account).Updates(map[string]any{
			"available_credits": gorm.Expr("available_credits + ?", request.Credits),
			"depleted_at":       nil,
			"updated_at":        now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", account.ID).First(&account).Error; err != nil {
			return err
		}
		inserted = true
		return createCreditLedgerEntry(tx, store.CreditLedgerEntry{
			EntryID:      "ledger_" + request.GrantID,
			GuildID:      account.GuildID,
			AccountID:    account.ID,
			GrantID:      request.GrantID,
			Type:         "grant",
			Action:       "pack_grant",
			Credits:      request.Credits,
			BalanceAfter: account.AvailableCredits,
			MetadataJSON: request.MetadataJSON,
			CreatedAt:    now,
		})
	})
	return grant, account, inserted, err
}

func (r *BillingRepository) BeginCreditReservation(ctx context.Context, request CreditReservationRequest, now time.Time) (store.CreditReservation, store.CreditAccount, bool, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	request.ReservationID = strings.TrimSpace(request.ReservationID)
	request.GuildID = strings.TrimSpace(request.GuildID)
	request.Action = strings.TrimSpace(request.Action)
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.MetadataJSON = normalizedJSON(request.MetadataJSON)
	if request.ExpiresAt.IsZero() {
		request.ExpiresAt = now.Add(30 * time.Minute)
	}
	if request.ReservationID == "" || request.GuildID == "" || request.Action == "" || request.ExpectedCredits <= 0 || request.MaxCredits <= 0 {
		return store.CreditReservation{}, store.CreditAccount{}, false, fmt.Errorf("credit reservation is missing required fields")
	}
	if request.MaxCredits < request.ExpectedCredits {
		request.MaxCredits = request.ExpectedCredits
	}

	var reservation store.CreditReservation
	var account store.CreditAccount
	denied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := releaseExpiredCreditReservationsTx(tx, request.GuildID, now); err != nil {
			return err
		}
		if _, err := expireCreditGrantsTx(tx, request.GuildID, now); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guild_id = ?", request.GuildID).First(&account).Error; err != nil {
			return err
		}
		allocations, ok, err := reserveCreditsFromGrantsTx(tx, account.ID, request.GuildID, request.MaxCredits, now)
		if err != nil || !ok {
			denied = !ok
			return err
		}
		if err := tx.Model(&account).Updates(map[string]any{
			"available_credits": gorm.Expr("CASE WHEN available_credits >= ? THEN available_credits - ? ELSE 0 END", request.MaxCredits, request.MaxCredits),
			"reserved_credits":  gorm.Expr("reserved_credits + ?", request.MaxCredits),
			"depleted_at":       depletedAtExpression(now),
			"updated_at":        now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", account.ID).First(&account).Error; err != nil {
			return err
		}
		reservation = store.CreditReservation{
			ReservationID:   request.ReservationID,
			GuildID:         request.GuildID,
			AccountID:       account.ID,
			Action:          request.Action,
			RequestID:       request.RequestID,
			ExpectedCredits: request.ExpectedCredits,
			MaxCredits:      request.MaxCredits,
			Status:          "pending",
			ExpiresAt:       request.ExpiresAt.UTC(),
			MetadataJSON:    reservationMetadataJSON(request.MetadataJSON, allocations, nil),
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := tx.Create(&reservation).Error; err != nil {
			return err
		}
		return createCreditLedgerEntry(tx, store.CreditLedgerEntry{
			EntryID:       "ledger_reserve_" + request.ReservationID,
			GuildID:       account.GuildID,
			AccountID:     account.ID,
			ReservationID: request.ReservationID,
			Type:          "reserve",
			Action:        request.Action,
			Credits:       -request.MaxCredits,
			BalanceAfter:  account.AvailableCredits,
			RequestID:     request.RequestID,
			MetadataJSON:  request.MetadataJSON,
			CreatedAt:     now,
		})
	})
	return reservation, account, denied, err
}

func (r *BillingRepository) CommitCreditReservation(ctx context.Context, request CreditCommitRequest, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	request.ReservationID = strings.TrimSpace(request.ReservationID)
	request.MetadataJSON = normalizedJSON(request.MetadataJSON)
	if request.ReservationID == "" {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reservation store.CreditReservation
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("reservation_id = ?", request.ReservationID).First(&reservation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if reservation.Status != "pending" {
			return nil
		}
		finalCredits := request.FinalCredits
		if finalCredits <= 0 {
			finalCredits = reservation.ExpectedCredits
		}
		var account store.CreditAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", reservation.AccountID).First(&account).Error; err != nil {
			return err
		}
		metadata, err := decodeReservationMetadata(reservation.MetadataJSON)
		if err != nil {
			return err
		}
		extra := int64(0)
		if finalCredits > reservation.MaxCredits {
			extra = finalCredits - reservation.MaxCredits
			extraAllocations, ok, err := reserveCreditsFromGrantsTx(tx, account.ID, reservation.GuildID, extra, now)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("insufficient credits for final credit settlement")
			}
			metadata.Allocations = append(metadata.Allocations, extraAllocations...)
		}
		unused := reservation.MaxCredits - finalCredits
		if unused < 0 {
			unused = 0
		}
		restored, err := restoreCreditAllocationsTx(tx, metadata.Allocations, unused, now)
		if err != nil {
			return err
		}
		availableDelta := restored - extra
		if err := tx.Model(&account).Updates(map[string]any{
			"available_credits": gorm.Expr("available_credits + ?", availableDelta),
			"reserved_credits":  gorm.Expr("CASE WHEN reserved_credits >= ? THEN reserved_credits - ? ELSE 0 END", reservation.MaxCredits, reservation.MaxCredits),
			"depleted_at":       depletedAtExpression(now),
			"updated_at":        now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", account.ID).First(&account).Error; err != nil {
			return err
		}
		metadata.Commit = map[string]any{"final_credits": finalCredits}
		if request.MetadataJSON != "{}" {
			metadata.Commit["metadata"] = json.RawMessage(request.MetadataJSON)
		}
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		if err := tx.Model(&reservation).Updates(map[string]any{
			"status":            "committed",
			"committed_credits": finalCredits,
			"metadata_json":     string(encoded),
			"updated_at":        now,
		}).Error; err != nil {
			return err
		}
		if unused > 0 && restored > 0 {
			if err := createCreditLedgerEntry(tx, store.CreditLedgerEntry{
				EntryID:       "ledger_release_unused_" + reservation.ReservationID,
				GuildID:       reservation.GuildID,
				AccountID:     account.ID,
				ReservationID: reservation.ReservationID,
				Type:          "release",
				Action:        reservation.Action,
				Credits:       restored,
				BalanceAfter:  account.AvailableCredits,
				RequestID:     reservation.RequestID,
				MetadataJSON:  `{"reason":"unused_reserved_credits"}`,
				CreatedAt:     now,
			}); err != nil {
				return err
			}
		}
		return createCreditLedgerEntry(tx, store.CreditLedgerEntry{
			EntryID:       "ledger_commit_" + reservation.ReservationID,
			GuildID:       reservation.GuildID,
			AccountID:     account.ID,
			ReservationID: reservation.ReservationID,
			Type:          "commit",
			Action:        reservation.Action,
			Credits:       -finalCredits,
			BalanceAfter:  account.AvailableCredits,
			RequestID:     reservation.RequestID,
			MetadataJSON:  request.MetadataJSON,
			CreatedAt:     now,
		})
	})
}

func (r *BillingRepository) ReleaseCreditReservation(ctx context.Context, reservationID string, reason string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	reservationID = strings.TrimSpace(reservationID)
	if reservationID == "" {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return releaseCreditReservationTx(tx, reservationID, strings.TrimSpace(reason), now)
	})
}

func (r *BillingRepository) ExpireCreditReservations(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	var released int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reservations []store.CreditReservation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("status = ? AND expires_at <= ?", "pending", now).
			Find(&reservations).Error; err != nil {
			return err
		}
		for _, reservation := range reservations {
			if err := releaseCreditReservationTx(tx, reservation.ReservationID, "expired", now); err != nil {
				return err
			}
			released++
		}
		return nil
	})
	return released, err
}

func (r *BillingRepository) ExpireCreditGrants(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	var expired int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		expired, err = expireCreditGrantsTx(tx, "", now)
		return err
	})
	return expired, err
}

func (r *BillingRepository) ExpireCreditGrantsForGuild(ctx context.Context, guildID string, now time.Time) (int64, error) {
	guildID = strings.TrimSpace(guildID)
	if guildID == "" {
		return 0, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	var expired int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		expired, err = expireCreditGrantsTx(tx, guildID, now)
		return err
	})
	return expired, err
}

func expireCreditGrantsTx(tx *gorm.DB, guildID string, now time.Time) (int64, error) {
	var grants []store.CreditGrant
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("credits_remaining > 0 AND expires_at IS NOT NULL AND expires_at <= ?", now)
	if guildID = strings.TrimSpace(guildID); guildID != "" {
		query = query.Where("guild_id = ?", guildID)
	}
	if err := query.Find(&grants).Error; err != nil {
		return 0, err
	}

	var expired int64
	for _, grant := range grants {
		var account store.CreditAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", grant.AccountID).First(&account).Error; err != nil {
			return expired, err
		}
		credits := grant.CreditsRemaining
		if err := tx.Model(&grant).Updates(map[string]any{
			"credits_remaining": 0,
			"updated_at":        now,
		}).Error; err != nil {
			return expired, err
		}
		if err := tx.Model(&account).Updates(map[string]any{
			"available_credits": gorm.Expr("CASE WHEN available_credits >= ? THEN available_credits - ? ELSE 0 END", credits, credits),
			"depleted_at":       depletedAtExpression(now),
			"updated_at":        now,
		}).Error; err != nil {
			return expired, err
		}
		if err := tx.Where("id = ?", account.ID).First(&account).Error; err != nil {
			return expired, err
		}
		if err := createCreditLedgerEntry(tx, store.CreditLedgerEntry{
			EntryID:      fmt.Sprintf("ledger_expiry_%s_%d", grant.GrantID, now.UnixNano()),
			GuildID:      grant.GuildID,
			AccountID:    grant.AccountID,
			GrantID:      grant.GrantID,
			Type:         "expiry",
			Action:       "credit_expiry",
			Credits:      -credits,
			BalanceAfter: account.AvailableCredits,
			MetadataJSON: `{"reason":"grant_expired"}`,
			CreatedAt:    now,
		}); err != nil {
			return expired, err
		}
		expired++
	}
	return expired, nil
}

func reserveCreditsFromGrantsTx(tx *gorm.DB, accountID uint, guildID string, credits int64, now time.Time) ([]creditAllocation, bool, error) {
	if credits <= 0 {
		return nil, true, nil
	}
	var grants []store.CreditGrant
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("account_id = ? AND guild_id = ? AND credits_remaining > 0 AND (expires_at IS NULL OR expires_at > ?)", accountID, guildID, now).
		Order("CASE WHEN expires_at IS NULL THEN 1 ELSE 0 END ASC").
		Order("expires_at ASC").
		Order("id ASC").
		Find(&grants).Error; err != nil {
		return nil, false, err
	}
	remaining := credits
	allocations := make([]creditAllocation, 0, len(grants))
	for _, grant := range grants {
		if remaining <= 0 {
			break
		}
		take := grant.CreditsRemaining
		if take > remaining {
			take = remaining
		}
		if take <= 0 {
			continue
		}
		allocations = append(allocations, creditAllocation{GrantID: grant.GrantID, Credits: take})
		remaining -= take
	}
	if remaining > 0 {
		return nil, false, nil
	}
	for _, allocation := range allocations {
		if err := tx.Model(&store.CreditGrant{}).
			Where("grant_id = ?", allocation.GrantID).
			Updates(map[string]any{
				"credits_remaining": gorm.Expr("credits_remaining - ?", allocation.Credits),
				"updated_at":        now,
			}).Error; err != nil {
			return nil, false, err
		}
	}
	return allocations, true, nil
}

func restoreCreditAllocationsTx(tx *gorm.DB, allocations []creditAllocation, credits int64, now time.Time) (int64, error) {
	if credits <= 0 {
		return 0, nil
	}
	remaining := credits
	restored := int64(0)
	for index := len(allocations) - 1; index >= 0 && remaining > 0; index-- {
		allocation := allocations[index]
		restore := allocation.Credits
		if restore > remaining {
			restore = remaining
		}
		if restore <= 0 {
			continue
		}
		result := tx.Model(&store.CreditGrant{}).
			Where("grant_id = ? AND (expires_at IS NULL OR expires_at > ?)", allocation.GrantID, now).
			Updates(map[string]any{
				"credits_remaining": gorm.Expr("credits_remaining + ?", restore),
				"updated_at":        now,
			})
		if result.Error != nil {
			return restored, result.Error
		}
		if result.RowsAffected > 0 {
			restored += restore
		}
		remaining -= restore
	}
	return restored, nil
}

func releaseExpiredCreditReservationsTx(tx *gorm.DB, guildID string, now time.Time) error {
	var reservations []store.CreditReservation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("guild_id = ? AND status = ? AND expires_at <= ?", strings.TrimSpace(guildID), "pending", now).
		Find(&reservations).Error; err != nil {
		return err
	}
	for _, reservation := range reservations {
		if err := releaseCreditReservationTx(tx, reservation.ReservationID, "expired", now); err != nil {
			return err
		}
	}
	return nil
}

func releaseCreditReservationTx(tx *gorm.DB, reservationID string, reason string, now time.Time) error {
	var reservation store.CreditReservation
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("reservation_id = ?", strings.TrimSpace(reservationID)).First(&reservation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if reservation.Status != "pending" {
		return nil
	}
	var account store.CreditAccount
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", reservation.AccountID).First(&account).Error; err != nil {
		return err
	}
	metadata, err := decodeReservationMetadata(reservation.MetadataJSON)
	if err != nil {
		return err
	}
	restored, err := restoreCreditAllocationsTx(tx, metadata.Allocations, reservation.MaxCredits, now)
	if err != nil {
		return err
	}
	if err := tx.Model(&account).Updates(map[string]any{
		"available_credits": gorm.Expr("available_credits + ?", restored),
		"reserved_credits":  gorm.Expr("CASE WHEN reserved_credits >= ? THEN reserved_credits - ? ELSE 0 END", reservation.MaxCredits, reservation.MaxCredits),
		"depleted_at":       depletedAtExpression(now),
		"updated_at":        now,
	}).Error; err != nil {
		return err
	}
	if err := tx.Where("id = ?", account.ID).First(&account).Error; err != nil {
		return err
	}
	if reason == "" {
		reason = "released"
	}
	metadata.Release = map[string]string{"reason": reason}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	status := "released"
	if reason == "expired" {
		status = "expired"
	}
	if err := tx.Model(&reservation).Updates(map[string]any{
		"status":        status,
		"metadata_json": string(encoded),
		"updated_at":    now,
	}).Error; err != nil {
		return err
	}
	return createCreditLedgerEntry(tx, store.CreditLedgerEntry{
		EntryID:       "ledger_release_" + reservation.ReservationID,
		GuildID:       reservation.GuildID,
		AccountID:     account.ID,
		ReservationID: reservation.ReservationID,
		Type:          "release",
		Action:        reservation.Action,
		Credits:       restored,
		BalanceAfter:  account.AvailableCredits,
		RequestID:     reservation.RequestID,
		MetadataJSON:  marshalLedgerMetadata(map[string]string{"reason": reason}),
		CreatedAt:     now,
	})
}

func createCreditLedgerEntry(tx *gorm.DB, entry store.CreditLedgerEntry) error {
	entry.EntryID = strings.TrimSpace(entry.EntryID)
	entry.GuildID = strings.TrimSpace(entry.GuildID)
	entry.ReservationID = strings.TrimSpace(entry.ReservationID)
	entry.GrantID = strings.TrimSpace(entry.GrantID)
	entry.Type = strings.TrimSpace(entry.Type)
	entry.Action = strings.TrimSpace(entry.Action)
	entry.RequestID = strings.TrimSpace(entry.RequestID)
	entry.MetadataJSON = normalizedJSON(entry.MetadataJSON)
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	if entry.EntryID == "" || entry.GuildID == "" || entry.AccountID == 0 || entry.Type == "" {
		return fmt.Errorf("credit ledger entry is missing required fields")
	}
	err := tx.Create(&entry).Error
	if isBillingUniqueConstraintError(err) {
		return nil
	}
	return err
}

func reservationMetadataJSON(metadataJSON string, allocations []creditAllocation, release map[string]string) string {
	metadataJSON = normalizedJSON(metadataJSON)
	envelope := creditReservationMetadata{
		Metadata:    json.RawMessage(metadataJSON),
		Allocations: allocations,
		Release:     release,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return `{"allocations":[]}`
	}
	return string(data)
}

func decodeReservationMetadata(raw string) (creditReservationMetadata, error) {
	var metadata creditReservationMetadata
	if strings.TrimSpace(raw) == "" {
		return creditReservationMetadata{}, nil
	}
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return creditReservationMetadata{}, err
	}
	return metadata, nil
}

func normalizedJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	if !json.Valid([]byte(raw)) {
		return "{}"
	}
	return raw
}

func marshalLedgerMetadata(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func depletedAtExpression(now time.Time) clause.Expr {
	return gorm.Expr("CASE WHEN available_credits <= 0 AND depleted_at IS NULL THEN ? WHEN available_credits > 0 THEN NULL ELSE depleted_at END", now)
}

func nonZeroInt(value, fallback int) int {
	if value != 0 {
		return value
	}
	return fallback
}

func nonZeroInt64(value, fallback int64) int64 {
	if value != 0 {
		return value
	}
	return fallback
}

func firstCreditText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
