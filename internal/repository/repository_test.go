package repository

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sn0w/panda2/internal/store"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGuildConfigEnsureDefaultIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewGuildConfigRepository(db.DB)
	config, err := repo.EnsureDefault(ctx, "guild-1")
	if err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if config.ToolPolicy != "admin_only" || !config.MemoryEnabled {
		t.Fatalf("unexpected defaults: tool_policy=%q memory_enabled=%t", config.ToolPolicy, config.MemoryEnabled)
	}

	again, err := repo.EnsureDefault(ctx, "guild-1")
	if err != nil {
		t.Fatalf("EnsureDefault again: %v", err)
	}
	if again.GuildID != "guild-1" || again.ToolPolicy != config.ToolPolicy {
		t.Fatalf("expected existing config to remain, got %+v", again)
	}
}

func TestGuildConfigMissingReadDoesNotLogRecordNotFound(t *testing.T) {
	ctx := context.Background()
	db, logs, cleanup := newRepositoryGormWithLogBuffer(t)
	defer cleanup()

	repo := NewGuildConfigRepository(db)
	if _, ok, err := repo.Get(ctx, "guild-1"); err != nil || ok {
		t.Fatalf("expected missing guild config without error, ok=%t err=%v", ok, err)
	}
	if strings.Contains(logs.String(), "record not found") {
		t.Fatalf("missing guild config should not be logged as record not found:\n%s", logs.String())
	}
}

func TestGuildConfigEnsureDefaultCreatesWithoutRecordNotFoundLog(t *testing.T) {
	ctx := context.Background()
	db, logs, cleanup := newRepositoryGormWithLogBuffer(t)
	defer cleanup()

	repo := NewGuildConfigRepository(db)
	config, err := repo.EnsureDefault(ctx, "guild-1")
	if err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if config.GuildID != "guild-1" {
		t.Fatalf("unexpected config: %+v", config)
	}
	if strings.Contains(logs.String(), "record not found") {
		t.Fatalf("default guild config creation should not be logged as record not found:\n%s", logs.String())
	}
}

func TestRuntimeStatusDefaultsAndUpdate(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewRuntimeStatusRepository(db.DB)
	status, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status.Key != RuntimeStatusGlobalKey || status.Disabled || status.Message != "" || status.UpdatedBy != "" {
		t.Fatalf("unexpected default runtime status: %+v", status)
	}

	updated, err := repo.Update(ctx, true, "Panda is napping.", "treasury_wallet:test")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !updated.Disabled || updated.Message != "Panda is napping." || updated.UpdatedBy != "treasury_wallet:test" {
		t.Fatalf("unexpected updated runtime status: %+v", updated)
	}

	again, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if again.Key != RuntimeStatusGlobalKey || !again.Disabled || again.Message != updated.Message {
		t.Fatalf("expected persisted runtime status, got %+v", again)
	}
}

func TestGuildMissingReadDoesNotLogRecordNotFound(t *testing.T) {
	ctx := context.Background()
	db, logs, cleanup := newRepositoryGormWithLogBuffer(t)
	defer cleanup()

	repo := NewGuildRepository(db)
	if _, ok, err := repo.Get(ctx, "guild-1"); err != nil || ok {
		t.Fatalf("expected missing guild without error, ok=%t err=%v", ok, err)
	}
	if strings.Contains(logs.String(), "record not found") {
		t.Fatalf("missing guild should not be logged as record not found:\n%s", logs.String())
	}
}

func TestGuildRecordAuthorizedInstallCreatesWithoutRecordNotFoundLog(t *testing.T) {
	ctx := context.Background()
	db, logs, cleanup := newRepositoryGormWithLogBuffer(t)
	defer cleanup()

	repo := NewGuildRepository(db)
	guild, err := repo.RecordAuthorizedInstall(ctx, GuildInstall{
		GuildID:           "guild-1",
		Name:              "Panda Server",
		OwnerUserID:       "owner-1",
		InstalledByUserID: "installer-1",
	})
	if err != nil {
		t.Fatalf("RecordAuthorizedInstall: %v", err)
	}
	if guild.GuildID != "guild-1" || guild.InstallStatus != GuildInstallStatusActive {
		t.Fatalf("unexpected guild: %+v", guild)
	}
	if strings.Contains(logs.String(), "record not found") {
		t.Fatalf("initial guild install should not be logged as record not found:\n%s", logs.String())
	}
}

func TestUsageRecord(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewUsageRepository(db.DB)
	if err := repo.Record(ctx, store.UsageEvent{UserID: "user-1", Command: "ask", Success: true}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	count, err := repo.CountByUser(ctx, "user-1")
	if err != nil {
		t.Fatalf("CountByUser: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one usage event, got %d", count)
	}
}

func TestUserSafetyStrikesTimeoutAndResetsAfterExpiry(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewUserSafetyRepository(db.DB)
	now := time.Date(2026, 6, 25, 19, 0, 0, 0, time.UTC)
	for i := 1; i <= 2; i++ {
		status, err := repo.AddStrike(ctx, "guild-1", "user-1", 3, 10*time.Minute, now.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("AddStrike %d: %v", i, err)
		}
		if status.TimedOut || status.State.ActiveStrikes != i || status.State.TotalStrikes != i {
			t.Fatalf("unexpected strike %d status: %+v", i, status)
		}
	}

	status, err := repo.AddStrike(ctx, "guild-1", "user-1", 3, 10*time.Minute, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("AddStrike 3: %v", err)
	}
	if !status.TimedOut || status.State.ActiveStrikes != 0 || status.State.TotalStrikes != 3 || status.State.TimeoutUntil == nil {
		t.Fatalf("expected third strike to time out and reset active strikes, got %+v", status)
	}

	stillTimedOut, err := repo.Status(ctx, "guild-1", "user-1", now.Add(12*time.Minute))
	if err != nil {
		t.Fatalf("Status during timeout: %v", err)
	}
	if !stillTimedOut.TimedOut {
		t.Fatalf("expected timeout to remain active, got %+v", stillTimedOut)
	}

	expired, err := repo.Status(ctx, "guild-1", "user-1", now.Add(14*time.Minute))
	if err != nil {
		t.Fatalf("Status after timeout: %v", err)
	}
	if expired.TimedOut || expired.State.ActiveStrikes != 0 || expired.State.TimeoutUntil != nil || expired.State.TotalStrikes != 3 {
		t.Fatalf("expected expired timeout to clear active state, got %+v", expired)
	}
}

func TestUserSafetyStrikesAreScopedByGuildAndUser(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewUserSafetyRepository(db.DB)
	now := time.Date(2026, 6, 25, 19, 30, 0, 0, time.UTC)

	for i := 0; i < 2; i++ {
		if _, err := repo.AddStrike(ctx, "guild-1", "user-1", 3, 10*time.Minute, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("AddStrike user-1 guild-1 %d: %v", i+1, err)
		}
	}
	if _, err := repo.AddStrike(ctx, "guild-1", "user-2", 3, 10*time.Minute, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("AddStrike user-2 guild-1: %v", err)
	}
	if _, err := repo.AddStrike(ctx, "guild-2", "user-1", 3, 10*time.Minute, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("AddStrike user-1 guild-2: %v", err)
	}

	userOneGuildOne, err := repo.Status(ctx, "guild-1", "user-1", now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("Status user-1 guild-1: %v", err)
	}
	if userOneGuildOne.TimedOut || userOneGuildOne.State.ActiveStrikes != 2 || userOneGuildOne.State.TotalStrikes != 2 {
		t.Fatalf("expected guild-1/user-1 to keep two strikes, got %+v", userOneGuildOne)
	}

	userTwoGuildOne, err := repo.Status(ctx, "guild-1", "user-2", now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("Status user-2 guild-1: %v", err)
	}
	if userTwoGuildOne.TimedOut || userTwoGuildOne.State.ActiveStrikes != 1 || userTwoGuildOne.State.TotalStrikes != 1 {
		t.Fatalf("expected guild-1/user-2 to keep one isolated strike, got %+v", userTwoGuildOne)
	}

	userOneGuildTwo, err := repo.Status(ctx, "guild-2", "user-1", now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("Status user-1 guild-2: %v", err)
	}
	if userOneGuildTwo.TimedOut || userOneGuildTwo.State.ActiveStrikes != 1 || userOneGuildTwo.State.TotalStrikes != 1 {
		t.Fatalf("expected guild-2/user-1 to keep one isolated strike, got %+v", userOneGuildTwo)
	}
}

func TestUserSafetyRemoveStrikeDecrementsActiveAndTotal(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewUserSafetyRepository(db.DB)
	now := time.Date(2026, 6, 25, 20, 0, 0, 0, time.UTC)
	if _, err := repo.AddStrike(ctx, "guild-1", "user-1", 3, 10*time.Minute, now); err != nil {
		t.Fatalf("AddStrike: %v", err)
	}

	status, err := repo.RemoveStrike(ctx, "guild-1", "user-1", 1, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("RemoveStrike: %v", err)
	}
	if status.TimedOut || status.State.ActiveStrikes != 0 || status.State.TotalStrikes != 0 || status.State.LastStrikeAt != nil || status.State.TimeoutUntil != nil {
		t.Fatalf("expected strike state to be cleared, got %+v", status)
	}
	listed, err := repo.List(ctx, "guild-1", 25)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("cleared safety state should not appear in list, got %+v", listed)
	}
	if _, err := repo.RemoveStrike(ctx, "guild-1", "user-1", 1, now.Add(2*time.Minute)); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound removing an empty safety state, got %v", err)
	}
}

func TestUserSafetyRemoveStrikeClearsTimeout(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewUserSafetyRepository(db.DB)
	now := time.Date(2026, 6, 25, 20, 30, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, err := repo.AddStrike(ctx, "guild-1", "user-1", 3, 10*time.Minute, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("AddStrike %d: %v", i+1, err)
		}
	}

	status, err := repo.RemoveStrike(ctx, "guild-1", "user-1", 1, now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("RemoveStrike: %v", err)
	}
	if status.TimedOut || status.State.TimeoutUntil != nil || status.State.TotalStrikes != 2 {
		t.Fatalf("expected timeout cleared and total decremented, got %+v", status)
	}

	cleared, err := repo.Clear(ctx, "guild-1", "user-1", now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if cleared.State.ActiveStrikes != 0 || cleared.State.TotalStrikes != 0 || cleared.State.LastStrikeAt != nil || cleared.State.TimeoutUntil != nil {
		t.Fatalf("expected all safety state cleared, got %+v", cleared)
	}
}

func TestUserSafetySetTimeoutUsesRequestedDuration(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewUserSafetyRepository(db.DB)
	now := time.Date(2026, 6, 25, 21, 0, 0, 0, time.UTC)
	status, err := repo.SetTimeout(ctx, "guild-1", "user-1", 30*time.Minute, now)
	if err != nil {
		t.Fatalf("SetTimeout: %v", err)
	}
	if !status.TimedOut || status.State.TimeoutUntil == nil || !status.State.TimeoutUntil.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("expected 30 minute timeout, got %+v", status)
	}
	if status.State.ActiveStrikes != 0 || status.State.TotalStrikes != 0 || status.State.LastStrikeAt != nil {
		t.Fatalf("manual timeout should not add strikes, got %+v", status)
	}

	active, err := repo.Status(ctx, "guild-1", "user-1", now.Add(29*time.Minute))
	if err != nil {
		t.Fatalf("Status during timeout: %v", err)
	}
	if !active.TimedOut {
		t.Fatalf("expected timeout to remain active, got %+v", active)
	}
	expired, err := repo.Status(ctx, "guild-1", "user-1", now.Add(31*time.Minute))
	if err != nil {
		t.Fatalf("Status after timeout: %v", err)
	}
	if expired.TimedOut || expired.State.TimeoutUntil != nil {
		t.Fatalf("expected expired timeout to clear, got %+v", expired)
	}
}

func TestCreditReservationCommitSettlesAboveInitialHold(t *testing.T) {
	ctx := context.Background()
	repo, _, cleanup := newBillingRepositoryWithLogBuffer(t)
	defer cleanup()

	now := time.Date(2026, 6, 22, 22, 43, 8, 0, time.UTC)
	seedCreditAccountAndGrant(t, ctx, repo, "guild-1", "grant-1", 20, now)

	reservation, account, denied, err := repo.BeginCreditReservation(ctx, CreditReservationRequest{
		ReservationID:   "reservation-1",
		GuildID:         "guild-1",
		Action:          "assistant_model_round",
		RequestID:       "request-1",
		ExpectedCredits: 4,
		MaxCredits:      4,
	}, now)
	if err != nil {
		t.Fatalf("BeginCreditReservation: %v", err)
	}
	if denied {
		t.Fatal("expected credit reservation to be allowed")
	}
	if reservation.ReservationID == "" || account.AvailableCredits != 16 || account.ReservedCredits != 4 {
		t.Fatalf("expected 4-credit hold, reservation=%+v account=%+v", reservation, account)
	}

	if err := repo.CommitCreditReservation(ctx, CreditCommitRequest{
		ReservationID: reservation.ReservationID,
		FinalCredits:  7,
		MetadataJSON:  `{"prompt_tokens":8000}`,
	}, now.Add(time.Minute)); err != nil {
		t.Fatalf("CommitCreditReservation: %v", err)
	}

	account, ok, err := repo.GetCreditAccountByGuild(ctx, "guild-1")
	if err != nil || !ok {
		t.Fatalf("GetCreditAccountByGuild: ok=%t err=%v", ok, err)
	}
	if account.AvailableCredits != 13 || account.ReservedCredits != 0 {
		t.Fatalf("expected final 7-credit debit, got account=%+v", account)
	}
	var grant store.CreditGrant
	if err := repo.db.Where("grant_id = ?", "grant-1").First(&grant).Error; err != nil {
		t.Fatalf("load credit grant: %v", err)
	}
	if grant.CreditsRemaining != 13 {
		t.Fatalf("expected grant remaining to track final debit, got %+v", grant)
	}
	var committed store.CreditReservation
	if err := repo.db.Where("reservation_id = ?", reservation.ReservationID).First(&committed).Error; err != nil {
		t.Fatalf("load credit reservation: %v", err)
	}
	if committed.Status != "committed" || committed.CommittedCredits != 7 {
		t.Fatalf("expected committed reservation for final credits, got %+v", committed)
	}
}

func TestCreditReservationReleaseRestoresHeldCredits(t *testing.T) {
	ctx := context.Background()
	repo, _, cleanup := newBillingRepositoryWithLogBuffer(t)
	defer cleanup()

	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	seedCreditAccountAndGrant(t, ctx, repo, "guild-1", "grant-1", 10, now)

	reservation, _, denied, err := repo.BeginCreditReservation(ctx, CreditReservationRequest{
		ReservationID:   "reservation-1",
		GuildID:         "guild-1",
		Action:          "web_search",
		RequestID:       "request-1",
		ExpectedCredits: 6,
		MaxCredits:      6,
	}, now)
	if err != nil {
		t.Fatalf("BeginCreditReservation: %v", err)
	}
	if denied {
		t.Fatal("expected credit reservation to be allowed")
	}

	if err := repo.ReleaseCreditReservation(ctx, reservation.ReservationID, "provider_error", now.Add(time.Minute)); err != nil {
		t.Fatalf("ReleaseCreditReservation: %v", err)
	}

	account, ok, err := repo.GetCreditAccountByGuild(ctx, "guild-1")
	if err != nil || !ok {
		t.Fatalf("GetCreditAccountByGuild: ok=%t err=%v", ok, err)
	}
	if account.AvailableCredits != 10 || account.ReservedCredits != 0 {
		t.Fatalf("expected released credits restored, got account=%+v", account)
	}
	var grant store.CreditGrant
	if err := repo.db.Where("grant_id = ?", "grant-1").First(&grant).Error; err != nil {
		t.Fatalf("load credit grant: %v", err)
	}
	if grant.CreditsRemaining != 10 {
		t.Fatalf("expected released grant allocation restored, got %+v", grant)
	}
}

func TestCreditReservationDenialDoesNotDrainGrantRows(t *testing.T) {
	ctx := context.Background()
	repo, _, cleanup := newBillingRepositoryWithLogBuffer(t)
	defer cleanup()

	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	seedCreditAccountAndGrant(t, ctx, repo, "guild-1", "grant-1", 5, now)

	_, account, denied, err := repo.BeginCreditReservation(ctx, CreditReservationRequest{
		ReservationID:   "reservation-1",
		GuildID:         "guild-1",
		Action:          "image_generation",
		RequestID:       "request-1",
		ExpectedCredits: 6,
		MaxCredits:      6,
	}, now)
	if err != nil {
		t.Fatalf("BeginCreditReservation: %v", err)
	}
	if !denied {
		t.Fatal("expected reservation to be denied")
	}
	if account.AvailableCredits != 5 || account.ReservedCredits != 0 {
		t.Fatalf("denied reservation should not update account, got %+v", account)
	}
	var grant store.CreditGrant
	if err := repo.db.Where("grant_id = ?", "grant-1").First(&grant).Error; err != nil {
		t.Fatalf("load credit grant: %v", err)
	}
	if grant.CreditsRemaining != 5 {
		t.Fatalf("denied reservation should not drain grants, got %+v", grant)
	}
}

func TestMusicEnsureSettingsCreatesWithoutRecordNotFoundLog(t *testing.T) {
	ctx := context.Background()
	db, logs, cleanup := newRepositoryGormWithLogBuffer(t)
	defer cleanup()

	repo := NewMusicRepository(db)
	settings, err := repo.EnsureSettings(ctx, "guild-1")
	if err != nil {
		t.Fatalf("EnsureSettings: %v", err)
	}
	if settings.GuildID != "guild-1" || settings.LoopMode != "off" || settings.DefaultVolume != 100 {
		t.Fatalf("unexpected music settings defaults: %+v", settings)
	}
	if strings.Contains(logs.String(), "record not found") {
		t.Fatalf("initial music settings creation should not be logged as record not found:\n%s", logs.String())
	}
}

func newBillingRepositoryWithLogBuffer(t *testing.T) (*BillingRepository, *bytes.Buffer, func()) {
	t.Helper()
	db, logs, cleanup := newRepositoryGormWithLogBuffer(t)
	return NewBillingRepository(db), logs, cleanup
}

func seedCreditAccountAndGrant(t *testing.T, ctx context.Context, repo *BillingRepository, guildID, grantID string, credits int64, now time.Time) {
	t.Helper()
	if _, err := repo.EnsureCreditAccount(ctx, store.CreditAccount{
		GuildID:                    guildID,
		Status:                     "active",
		ActivePack:                 "starter",
		RetentionDays:              30,
		KnowledgeStorageBytesLimit: 100 * 1024 * 1024,
		CreatedAt:                  now,
	}); err != nil {
		t.Fatalf("EnsureCreditAccount: %v", err)
	}
	expiresAt := now.Add(30 * 24 * time.Hour)
	if _, _, inserted, err := repo.GrantCredits(ctx, CreditGrantRequest{
		GrantID:   grantID,
		GuildID:   guildID,
		Source:    "test",
		SourceID:  grantID,
		Pack:      "starter",
		Credits:   credits,
		ExpiresAt: &expiresAt,
	}); err != nil {
		t.Fatalf("GrantCredits: %v", err)
	} else if !inserted {
		t.Fatalf("expected test grant %q to be inserted", grantID)
	}
}

func newRepositoryGormWithLogBuffer(t *testing.T) (*gorm.DB, *bytes.Buffer, func()) {
	t.Helper()
	var logs bytes.Buffer
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/billing-log.db"), &gorm.Config{
		Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn}),
	})
	if err != nil {
		t.Fatalf("open gorm db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrap sql db: %v", err)
	}
	if err := store.RunMigrations(db); err != nil {
		_ = sqlDB.Close()
		t.Fatalf("run migrations: %v", err)
	}
	return db, &logs, func() {
		_ = sqlDB.Close()
	}
}

func TestUsageBreakdownByCommandRejectsModelDimension(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewUsageRepository(db.DB)
	events := []store.UsageEvent{
		{GuildID: "guild-1", UserID: "user-1", ChannelID: "channel-1", Command: "ask", TotalTokens: 10, Success: true},
		{GuildID: "guild-1", UserID: "user-2", ChannelID: "channel-1", Command: "chat", TotalTokens: 20, Success: false},
		{GuildID: "guild-1", UserID: "user-2", ChannelID: "channel-2", Command: "ask", TotalTokens: 5, Success: true},
	}
	for _, event := range events {
		if err := repo.Record(ctx, event); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	rows, err := repo.BreakdownByGuild(ctx, "guild-1", time.Time{}, "command", 5)
	if err != nil {
		t.Fatalf("BreakdownByGuild: %v", err)
	}
	if len(rows) != 2 || rows[0].Label != "ask" || rows[0].TotalRequests != 2 || rows[0].Failed != 0 || rows[0].TotalTokens != 15 {
		t.Fatalf("unexpected breakdown rows: %+v", rows)
	}
	if _, err := repo.BreakdownByGuild(ctx, "guild-1", time.Time{}, "model", 5); err == nil {
		t.Fatal("expected unsupported dimension to fail")
	}
}

func TestMemberMemoryConsentDefaultsFalseAndUpdates(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewMemberRepository(db.DB)
	consent, err := repo.MemoryConsent(ctx, "guild-1", "user-1")
	if err != nil {
		t.Fatalf("MemoryConsent: %v", err)
	}
	if consent {
		t.Fatal("memory consent should default to false")
	}

	member, err := repo.SetMemoryConsent(ctx, "guild-1", "user-1", true)
	if err != nil {
		t.Fatalf("SetMemoryConsent true: %v", err)
	}
	if !member.MemoryConsent || !member.AssistantAllowed {
		t.Fatalf("unexpected member after enabling consent: %+v", member)
	}
	consent, err = repo.MemoryConsent(ctx, "guild-1", "user-1")
	if err != nil || !consent {
		t.Fatalf("expected enabled consent, got %t err=%v", consent, err)
	}

	member, err = repo.SetMemoryConsent(ctx, "guild-1", "user-1", false)
	if err != nil {
		t.Fatalf("SetMemoryConsent false: %v", err)
	}
	if member.MemoryConsent {
		t.Fatalf("expected disabled consent, got %+v", member)
	}
}

func TestKnowledgeSearchUsesLocalIndex(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewKnowledgeRepository(db.DB)
	document, err := repo.AddDocument(ctx, store.KnowledgeDocument{
		GuildID:   "guild-1",
		Title:     "Refund policy",
		CreatedBy: "admin",
	}, "Refunds are available within 14 days when a receipt is provided.")
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	results, err := repo.Search(ctx, "guild-1", "receipt refunds", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].DocumentID != document.ID || results[0].Title != "Refund policy" {
		t.Fatalf("unexpected result: %+v", results[0])
	}
}

func TestKnowledgeChunksDoNotSplitUTF8Runes(t *testing.T) {
	chunks := splitKnowledgeChunks("x" + strings.Repeat("界", 500))
	if len(chunks) < 2 {
		t.Fatalf("expected content to split into multiple chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk is not valid UTF-8: %q", chunk)
		}
	}
}

func TestAttachmentGetByGuild(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewAttachmentRepository(db.DB)
	attachment, err := repo.Record(ctx, store.Attachment{
		GuildID:       "guild-1",
		ChannelID:     "channel-1",
		MessageID:     "message-1",
		Filename:      "notes.md",
		ExtractedText: "Deploy after review.",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	found, err := repo.Get(ctx, "guild-1", attachment.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found.Filename != "notes.md" || found.ExtractedText == "" {
		t.Fatalf("unexpected attachment: %+v", found)
	}
	if _, err := repo.Get(ctx, "other-guild", attachment.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for other guild, got %v", err)
	}
}

func TestConversationRecentMessagesStayChronological(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewConversationRepository(db.DB)
	conversation, err := repo.GetOrCreateActive(ctx, ConversationKey{
		GuildID:     "guild-1",
		ChannelID:   "channel-1",
		OwnerUserID: "user-1",
		Title:       "question",
	})
	if err != nil {
		t.Fatalf("GetOrCreateActive: %v", err)
	}
	for _, content := range []string{"one", "two", "three"} {
		if err := repo.AppendMessage(ctx, store.AssistantMessage{
			ConversationID: conversation.ID,
			GuildID:        "guild-1",
			ChannelID:      "channel-1",
			UserID:         "user-1",
			Role:           "user",
			ContentPreview: content,
		}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	messages, err := repo.RecentMessages(ctx, conversation.ID, 2)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if got := []string{messages[0].ContentPreview, messages[1].ContentPreview}; got[0] != "two" || got[1] != "three" {
		t.Fatalf("unexpected recent messages: %v", got)
	}
}

func TestJobClaimAndRetry(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewJobRepository(db.DB)
	now := time.Now().UTC().Add(time.Hour)
	job, err := repo.Enqueue(ctx, store.Job{Kind: "summarize", GuildID: "guild-1", Payload: "{}"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	claimed, ok, err := repo.ClaimNext(ctx, "summarize", "worker-1", time.Minute, now)
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if !ok || claimed.ID != job.ID || claimed.Attempts != 1 || claimed.LockOwner != "worker-1" {
		t.Fatalf("unexpected claimed job: ok=%v job=%+v", ok, claimed)
	}
	if err := repo.Fail(ctx, claimed.ID, "temporary", 5*time.Minute, now); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	depth, err := repo.QueueDepth(ctx, "summarize")
	if err != nil {
		t.Fatalf("QueueDepth: %v", err)
	}
	if depth != 1 {
		t.Fatalf("expected retried job in queue, got depth %d", depth)
	}
}

func TestJobClaimReclaimsExpiredLease(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	repo := NewJobRepository(db.DB)
	job, err := repo.Enqueue(ctx, store.Job{Kind: "summarize", GuildID: "guild-1", Payload: "{}", MaxAttempts: 2, RunAfter: now.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, ok, err := repo.ClaimNext(ctx, "summarize", "worker-1", time.Minute, now)
	if err != nil || !ok {
		t.Fatalf("first ClaimNext ok=%t err=%v", ok, err)
	}
	if claimed.ID != job.ID || claimed.Attempts != 1 {
		t.Fatalf("unexpected first claim: %+v", claimed)
	}

	if _, ok, err := repo.ClaimNext(ctx, "summarize", "worker-2", time.Minute, now.Add(30*time.Second)); err != nil || ok {
		t.Fatalf("active lease should not be reclaimed: ok=%t err=%v", ok, err)
	}

	reclaimed, ok, err := repo.ClaimNext(ctx, "summarize", "worker-2", time.Minute, now.Add(2*time.Minute))
	if err != nil || !ok {
		t.Fatalf("expired ClaimNext ok=%t err=%v", ok, err)
	}
	if reclaimed.ID != job.ID || reclaimed.Attempts != 2 || reclaimed.LockOwner != "worker-2" {
		t.Fatalf("unexpected reclaimed job: %+v", reclaimed)
	}
}

func TestJobClaimFailsExpiredLeaseAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	repo := NewJobRepository(db.DB)
	job, err := repo.Enqueue(ctx, store.Job{Kind: "summarize", GuildID: "guild-1", Payload: "{}", MaxAttempts: 1, RunAfter: now.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, ok, err := repo.ClaimNext(ctx, "summarize", "worker-1", time.Minute, now); err != nil || !ok {
		t.Fatalf("first ClaimNext ok=%t err=%v", ok, err)
	}
	if _, ok, err := repo.ClaimNext(ctx, "summarize", "worker-2", time.Minute, now.Add(2*time.Minute)); err != nil || ok {
		t.Fatalf("maxed expired lease should not be reclaimed: ok=%t err=%v", ok, err)
	}

	var saved store.Job
	if err := db.DB.First(&saved, job.ID).Error; err != nil {
		t.Fatalf("lookup job: %v", err)
	}
	if saved.Status != "failed" || !strings.Contains(saved.LastError, "lease expired") || saved.LockOwner != "" || saved.LeaseExpiresAt != nil {
		t.Fatalf("expected failed expired job, got %+v", saved)
	}
}

func TestBudgetCheckAndConsumeUsesDurableWindow(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewBudgetRepository(db.DB)
	if _, err := repo.SetLimit(ctx, store.BudgetLimit{
		GuildID:       "guild-1",
		Scope:         BudgetScopeUser,
		SubjectID:     "user-1",
		Limit:         1,
		WindowSeconds: 3600,
	}); err != nil {
		t.Fatalf("SetLimit: %v", err)
	}

	now := time.Date(2026, 6, 19, 12, 30, 0, 0, time.UTC)
	request := BudgetCheckRequest{GuildID: "guild-1", UserID: "user-1", ChannelID: "channel-1", Now: now}
	if _, denied, err := repo.CheckAndConsume(ctx, request); err != nil || denied {
		t.Fatalf("first CheckAndConsume denied=%v err=%v", denied, err)
	}
	denial, denied, err := repo.CheckAndConsume(ctx, request)
	if err != nil {
		t.Fatalf("second CheckAndConsume: %v", err)
	}
	if !denied || denial.Scope != BudgetScopeUser {
		t.Fatalf("expected user budget denial, got denied=%v denial=%+v", denied, denial)
	}
	if denial.RetryAfter != 30*time.Minute {
		t.Fatalf("expected 30m retry, got %s", denial.RetryAfter)
	}
}

func TestComposedToolApprovedVersionsAreImmutable(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewComposedToolRepository(db.DB)
	record, err := repo.CreateDraft(ctx, store.ComposedTool{
		GuildID:   "guild-1",
		ToolID:    "guild-1:member_welcome",
		Name:      "member_welcome",
		Status:    "pending_approval",
		CreatedBy: "moderator-1",
	}, store.ComposedToolVersion{
		SpecJSON:           `{"schema_version":1}`,
		ValidationJSON:     `{"valid":true}`,
		ToolDefinitionJSON: `{"type":"function"}`,
		CreatedBy:          "moderator-1",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if _, err := repo.ApproveVersion(ctx, "guild-1", "member_welcome", 1, "admin-1"); err != nil {
		t.Fatalf("ApproveVersion: %v", err)
	}
	if err := repo.UpdateDraftVersion(ctx, record.Version.ID, `{"mutated":true}`, `{}`, `{}`); err == nil {
		t.Fatal("expected approved version mutation to fail")
	}

	next, err := repo.AddDraftVersion(ctx, record.Tool.ID, store.ComposedToolVersion{
		SpecJSON:           `{"schema_version":1,"name":"member_welcome"}`,
		ValidationJSON:     `{"valid":true}`,
		ToolDefinitionJSON: `{"type":"function"}`,
		CreatedBy:          "moderator-1",
	})
	if err != nil {
		t.Fatalf("AddDraftVersion: %v", err)
	}
	if next.VersionNumber != 2 {
		t.Fatalf("expected version 2, got %d", next.VersionNumber)
	}
	tool, ok, err := repo.GetByName(ctx, "guild-1", "member_welcome")
	if err != nil || !ok {
		t.Fatalf("GetByName: ok=%t err=%v", ok, err)
	}
	if tool.Status != "enabled" {
		t.Fatalf("new draft version should not disable current approved tool, got %s", tool.Status)
	}
}

func TestComposedToolDeleteByNameHardDeletesDependents(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repo := NewComposedToolRepository(db.DB)
	record, err := repo.CreateDraft(ctx, store.ComposedTool{
		GuildID:   "guild-1",
		ToolID:    "guild-1:member_welcome",
		Name:      "member_welcome",
		Status:    "pending_approval",
		CreatedBy: "moderator-1",
	}, store.ComposedToolVersion{
		SpecJSON:           `{"schema_version":1}`,
		ValidationJSON:     `{"valid":true}`,
		ToolDefinitionJSON: `{"type":"function"}`,
		CreatedBy:          "moderator-1",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if _, err := repo.ApproveVersion(ctx, "guild-1", "member_welcome", 1, "admin-1"); err != nil {
		t.Fatalf("ApproveVersion: %v", err)
	}
	if _, err := repo.CreateRun(ctx, store.ComposedToolRun{
		ComposedToolID: record.Tool.ID,
		VersionID:      record.Version.ID,
		GuildID:        "guild-1",
		InvocationType: "manual",
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := repo.TryDedupe(ctx, record.Tool.ID, "fingerprint-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("TryDedupe: %v", err)
	}

	deleted, err := repo.DeleteByName(ctx, "guild-1", "member_welcome")
	if err != nil {
		t.Fatalf("DeleteByName: %v", err)
	}
	if deleted.Name != "member_welcome" || deleted.ID != record.Tool.ID {
		t.Fatalf("unexpected deleted tool: %+v", deleted)
	}
	if _, ok, err := repo.GetByName(ctx, "guild-1", "member_welcome"); err != nil || ok {
		t.Fatalf("GetByName after delete ok=%t err=%v", ok, err)
	}
	for name, model := range map[string]any{
		"composed_tools":         &store.ComposedTool{},
		"composed_tool_versions": &store.ComposedToolVersion{},
		"composed_tool_runs":     &store.ComposedToolRun{},
		"composed_tool_dedupes":  &store.ComposedToolDedupe{},
	} {
		var count int64
		if err := db.DB.Model(model).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("expected %s to be empty after delete, got %d", name, count)
		}
	}
}
