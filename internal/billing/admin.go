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
// It exposes both the raw stored status (for editing) and the effective status.
type AdminGuildBilling struct {
	HasCreditAccount   bool
	Pack               string
	PackDisplayName    string
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
	Limits             PackDefinition
	AvailableCredits   int64
	ReservedCredits    int64
	Credits            int64
}

// AdminSetCreditAccountRequest captures an operator override of a guild credit
// account. Empty/nil fields leave the existing value untouched.
type AdminSetCreditAccountRequest struct {
	GuildID           string
	ActorUserID       string
	Pack              string
	Status            string
	PeriodEnd         *time.Time
	TrialEndsAt       *time.Time
	ClearTrialEndsAt  bool
	CancelAtPeriodEnd *bool
}

// AdminStatuses lists the credit account statuses an operator may assign.
func AdminStatuses() []string {
	return []string{StatusActive, StatusTrialing, StatusPastDue, StatusGrace, StatusReadOnly, StatusSuspended, StatusCanceled, StatusDepleted}
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
	case StatusDepleted:
		return GraceDepleted
	default:
		return GraceReadOnly
	}
}

// AdminOverview composes the operator billing view for a guild. A guild without
// a credit account yields HasCreditAccount=false so the caller can render it.
func (s *Service) AdminOverview(ctx context.Context, guildID string) (AdminGuildBilling, error) {
	guildID = strings.TrimSpace(guildID)
	if s == nil || s.repo == nil {
		return AdminGuildBilling{}, ErrNoCreditAccount
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

	account, ok, err := s.repo.GetCreditAccountByGuild(ctx, guildID)
	if err != nil {
		return AdminGuildBilling{}, err
	}
	if !ok {
		return overview, nil
	}

	pack, ok := PackForID(firstNonEmpty(account.ActivePack, PackTrial))
	if !ok {
		return AdminGuildBilling{}, ErrUnknownPack
	}
	entitlement := s.entitlementFromCreditAccount(account)

	overview.HasCreditAccount = true
	overview.Pack = pack.Pack
	overview.PackDisplayName = pack.DisplayName
	overview.Status = entitlement.Status
	overview.StoredStatus = strings.TrimSpace(account.Status)
	overview.GraceState = entitlement.GraceState
	overview.PaymentProvider = account.PaymentProvider
	overview.PeriodStart = entitlement.PeriodStart
	overview.PeriodEnd = entitlement.PeriodEnd
	overview.CanUsePaidFeatures = entitlement.CanUsePaidFeatures
	overview.ReadOnly = entitlement.ReadOnly
	overview.Limits = pack
	overview.AvailableCredits = account.AvailableCredits
	overview.ReservedCredits = account.ReservedCredits
	overview.Credits = pack.Credits
	if owner := strings.TrimSpace(account.BillingOwnerUserID); owner != "" {
		overview.BillingOwnerUserID = owner
	}
	return overview, nil
}

// AdminSetCreditAccount applies an operator override to a guild credit account.
func (s *Service) AdminSetCreditAccount(ctx context.Context, request AdminSetCreditAccountRequest) (AdminGuildBilling, error) {
	guildID := strings.TrimSpace(request.GuildID)
	if s == nil || s.repo == nil {
		return AdminGuildBilling{}, ErrNoCreditAccount
	}
	if guildID == "" {
		return AdminGuildBilling{}, fmt.Errorf("guild id is required")
	}

	now := s.currentTime()
	existing, hasExisting, err := s.repo.GetCreditAccountByGuild(ctx, guildID)
	if err != nil {
		return AdminGuildBilling{}, err
	}

	packID := ""
	if hasExisting {
		packID = existing.ActivePack
	}
	if requested := strings.TrimSpace(request.Pack); requested != "" {
		normalized, ok := NormalizePack(requested)
		if !ok {
			return AdminGuildBilling{}, ErrUnknownPack
		}
		packID = normalized
	}
	if packID == "" {
		packID = PackTrial
	}
	pack, ok := PackForID(packID)
	if !ok {
		return AdminGuildBilling{}, ErrUnknownPack
	}

	status := StatusActive
	if hasExisting && strings.TrimSpace(existing.Status) != "" {
		status = strings.TrimSpace(existing.Status)
	} else if packID == PackTrial {
		status = StatusTrialing
	}
	if requested := strings.ToLower(strings.TrimSpace(request.Status)); requested != "" {
		if !isAdminStatus(requested) {
			return AdminGuildBilling{}, fmt.Errorf("unsupported status %q", requested)
		}
		status = requested
	}
	grace := graceForStatus(status)

	billingOwner := ""
	if hasExisting {
		billingOwner = existing.BillingOwnerUserID
	}
	customer, err := s.repo.EnsureCustomerAccount(ctx, store.CustomerAccount{
		GuildID:            guildID,
		BillingOwnerUserID: billingOwner,
	})
	if err != nil {
		return AdminGuildBilling{}, err
	}
	if billingOwner == "" {
		billingOwner = strings.TrimSpace(customer.BillingOwnerUserID)
	}
	readOnlyAt := timePtrIf(status == StatusReadOnly || status == StatusCanceled, now)
	suspendedAt := timePtrIf(status == StatusSuspended, now)
	account, err := s.repo.EnsureCreditAccount(ctx, store.CreditAccount{
		GuildID:                    guildID,
		BillingOwnerUserID:         billingOwner,
		Status:                     status,
		PaymentProvider:            ProviderManual,
		ActivePack:                 pack.Pack,
		RetentionDays:              pack.RetentionDays,
		KnowledgeStorageBytesLimit: pack.KnowledgeStorageBytes,
		ReadOnlyAt:                 readOnlyAt,
		SuspendedAt:                suspendedAt,
	})
	if err != nil {
		return AdminGuildBilling{}, err
	}
	if !hasExisting || existing.ActivePack != pack.Pack {
		sourceID := fmt.Sprintf("admin:%s:%d", strings.TrimSpace(request.ActorUserID), now.UnixNano())
		_, account, _, err = s.repo.GrantCredits(ctx, repository.CreditGrantRequest{
			GrantID:      "grant_admin_" + safeLedgerID(guildID) + "_" + strconv.FormatInt(now.UnixNano(), 10),
			GuildID:      guildID,
			Source:       ProviderManual,
			SourceID:     sourceID,
			Pack:         pack.Pack,
			Credits:      pack.Credits,
			ExpiresAt:    timePtr(now.Add(pack.ExpiresAfter)),
			MetadataJSON: MarshalRaw(map[string]any{"pack": pack.Pack, "source": ProviderManual, "actor_user_id": request.ActorUserID}),
		})
		if err != nil {
			return AdminGuildBilling{}, err
		}
	}
	_ = account

	if s.audit != nil {
		metadata, err := json.Marshal(map[string]string{
			"pack":        pack.Pack,
			"status":      status,
			"grace_state": grace,
		})
		if err != nil {
			metadata = []byte("{}")
		}
		_ = s.audit.Record(ctx, store.AuditEvent{
			GuildID:    guildID,
			ActorID:    strings.TrimSpace(request.ActorUserID),
			Action:     "admin.credit_account.set",
			TargetType: "credit_account",
			TargetID:   guildID,
			Metadata:   string(metadata),
			CreatedAt:  now,
		})
	}

	return s.AdminOverview(ctx, guildID)
}

func timePtr(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func timePtrIf(ok bool, value time.Time) *time.Time {
	if !ok {
		return nil
	}
	return timePtr(value)
}
