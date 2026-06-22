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
}

func TestSolPaymentOrderVerificationRevealAndActivation(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	if _, err := service.EnsureTrial(ctx, TrialSeed{GuildID: "guild-1", BillingOwnerUserID: "owner-1", AuthorizedAt: now}); err != nil {
		t.Fatalf("EnsureTrial: %v", err)
	}

	order, err := service.CreateSolPaymentOrder(ctx, CreateSolPaymentOrderRequest{
		GuildID:            "guild-1",
		BillingOwnerUserID: "owner-1",
		Plan:               PlanPlus,
		SupportEmail:       "owner@example.com",
	})
	if err != nil {
		t.Fatalf("CreateSolPaymentOrder: %v", err)
	}
	if order.ExpectedLamports != 49_000_000 || order.DestinationWallet != "treasury-wallet" || order.Reference == "" || !strings.HasPrefix(order.PaymentURL, "solana:treasury-wallet?") {
		t.Fatalf("unexpected order view: %+v", order)
	}

	service.WithSolanaRPCClient(fakeSolanaRPCClient{transaction: verifiedTransaction(order, "payer-wallet", 50_000_000)})
	result, err := service.VerifySolPayment(ctx, VerifySolPaymentRequest{OrderID: order.OrderID, Signature: "sig-1"})
	if err != nil {
		t.Fatalf("VerifySolPayment: %v result=%+v", err, result)
	}
	if !result.Verified || result.Order.Status != SolOrderStatusVerified || result.Order.VerifiedTransactionSignature != "sig-1" {
		t.Fatalf("unexpected verification result: %+v", result)
	}

	reveal, err := service.RevealActivationKey(ctx, order.OrderID)
	if err != nil {
		t.Fatalf("RevealActivationKey: %v", err)
	}
	if !strings.HasPrefix(reveal.Key, "panda_sol_") || reveal.Prefix == "" {
		t.Fatalf("unexpected activation key reveal: %+v", reveal)
	}
	if _, err := service.RevealActivationKey(ctx, order.OrderID); !errors.Is(err, ErrActivationKeyAlreadyRevealed) {
		t.Fatalf("expected one-time reveal error, got %v", err)
	}

	activated, err := service.ActivateWithAPIKey(ctx, ActivateAPIKeyRequest{
		GuildID:     "guild-1",
		ActorUserID: "owner-1",
		APIKey:      reveal.Key,
	})
	if err != nil {
		t.Fatalf("ActivateWithAPIKey: %v", err)
	}
	if activated.Entitlement.Plan.Plan != PlanPlus || activated.Entitlement.PaymentProvider != ProviderSol || !activated.Entitlement.CanUsePaidFeatures {
		t.Fatalf("unexpected activated entitlement: %+v", activated.Entitlement)
	}
	if _, err := service.ActivateWithAPIKey(ctx, ActivateAPIKeyRequest{GuildID: "guild-1", ActorUserID: "owner-1", APIKey: reveal.Key}); !errors.Is(err, ErrActivationKeyConsumed) {
		t.Fatalf("expected consumed key error, got %v", err)
	}

	var event storepkg.InvoicePaymentEvent
	if err := database.DB.Where("provider = ? AND external_id = ?", ProviderSol, "sig-1").First(&event).Error; err != nil {
		t.Fatalf("load SOL payment event: %v", err)
	}
	if event.AmountLamports != 49_000_000 || event.Currency != "sol" || event.Status != "paid" {
		t.Fatalf("unexpected payment event: %+v", event)
	}
}

func TestSolVerificationRejectsWrongWalletAndKeepsOrderClosed(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	order, err := service.CreateSolPaymentOrder(ctx, CreateSolPaymentOrderRequest{
		GuildID: "guild-1",
		Plan:    PlanStarter,
	})
	if err != nil {
		t.Fatalf("CreateSolPaymentOrder: %v", err)
	}
	bad := order
	bad.DestinationWallet = "wrong-wallet"
	service.WithSolanaRPCClient(fakeSolanaRPCClient{transaction: verifiedTransaction(bad, "payer-wallet", 19_000_000)})

	result, err := service.VerifySolPayment(ctx, VerifySolPaymentRequest{OrderID: order.OrderID, Signature: "sig-wrong-wallet"})
	if !errors.Is(err, ErrSolPaymentVerificationFailed) {
		t.Fatalf("expected verification failure, got err=%v result=%+v", err, result)
	}
	if result.Verified || result.FailureCode != "verification_failed" {
		t.Fatalf("unexpected failed verification result: %+v", result)
	}
	if _, err := service.RevealActivationKey(ctx, order.OrderID); !errors.Is(err, ErrSolPaymentOrderNotVerified) {
		t.Fatalf("expected not verified reveal error, got %v", err)
	}

	var transactions int64
	if err := database.DB.Model(&storepkg.SolPaymentTransaction{}).Where("signature = ? AND status = ?", "sig-wrong-wallet", SolTransactionStatusFailed).Count(&transactions).Error; err != nil {
		t.Fatalf("count failed transactions: %v", err)
	}
	if transactions != 1 {
		t.Fatalf("expected one failed transaction record, got %d", transactions)
	}
}

func TestActivationKeyRevocationIsOperatorOnlyAndAudited(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	order, err := service.CreateSolPaymentOrder(ctx, CreateSolPaymentOrderRequest{
		GuildID:            "guild-1",
		BillingOwnerUserID: "owner-1",
		Plan:               PlanPro,
	})
	if err != nil {
		t.Fatalf("CreateSolPaymentOrder: %v", err)
	}
	service.WithSolanaRPCClient(fakeSolanaRPCClient{transaction: verifiedTransaction(order, "payer-wallet", 99_000_000)})
	if _, err := service.VerifySolPayment(ctx, VerifySolPaymentRequest{OrderID: order.OrderID, Signature: "sig-revoked"}); err != nil {
		t.Fatalf("VerifySolPayment: %v", err)
	}
	reveal, err := service.RevealActivationKey(ctx, order.OrderID)
	if err != nil {
		t.Fatalf("RevealActivationKey: %v", err)
	}

	if _, err := service.RevokeActivationAPIKey(ctx, RevokeActivationAPIKeyRequest{PaymentOrderID: order.OrderID, ActorUserID: "owner-1"}); !errors.Is(err, ErrBillingAccess) {
		t.Fatalf("expected operator access error, got %v", err)
	}
	revocation, err := service.RevokeActivationAPIKey(ctx, RevokeActivationAPIKeyRequest{
		PaymentOrderID:  order.OrderID,
		ActorUserID:     "operator-1",
		ActorIsOperator: true,
		Reason:          "support refund",
	})
	if err != nil {
		t.Fatalf("RevokeActivationAPIKey: %v", err)
	}
	if revocation.Prefix != reveal.Prefix || revocation.Order.OrderID != order.OrderID || revocation.RevokedAt.IsZero() {
		t.Fatalf("unexpected revocation result: %+v reveal=%+v", revocation, reveal)
	}
	if _, err := service.ActivateWithAPIKey(ctx, ActivateAPIKeyRequest{
		GuildID:     "guild-1",
		ActorUserID: "owner-1",
		APIKey:      reveal.Key,
	}); !errors.Is(err, ErrActivationKeyRevoked) {
		t.Fatalf("expected revoked key error, got %v", err)
	}

	for _, action := range []string{"billing.activation_key.created", "billing.activation_key.viewed", "billing.activation_key.revoked"} {
		var count int64
		if err := database.DB.Model(&storepkg.AuditEvent{}).Where("guild_id = ? AND action = ?", "guild-1", action).Count(&count).Error; err != nil {
			t.Fatalf("count audit action %s: %v", action, err)
		}
		if count != 1 {
			t.Fatalf("expected one audit action %s, got %d", action, count)
		}
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
		PublicURL:              "https://panda.example",
		SolanaRPCURL:           "https://api.devnet.solana.com",
		SolanaCluster:          "devnet",
		SolanaTreasuryWallet:   "treasury-wallet",
		SolanaConfirmation:     "finalized",
		SolanaOrderExpiration:  time.Hour,
		SolanaActivationKeyTTL: time.Hour,
		SolanaPlanLamports: map[string]int64{
			PlanStarter:  19_000_000,
			PlanPlus:     49_000_000,
			PlanPro:      99_000_000,
			PlanBusiness: 249_000_000,
		},
	}).WithAuditRecorder(repository.NewAuditRepository(database.DB))
	return service, database
}

type fakeSolanaRPCClient struct {
	transaction SolanaTransaction
	err         error
}

func (f fakeSolanaRPCClient) GetTransaction(context.Context, string, string) (SolanaTransaction, error) {
	if f.err != nil {
		return SolanaTransaction{}, f.err
	}
	return f.transaction, nil
}

func verifiedTransaction(order SolPaymentOrderView, payer string, lamports int64) SolanaTransaction {
	var transaction SolanaTransaction
	transaction.Transaction.Message.AccountKeys = []solanaAccountKey{{Pubkey: payer, Signer: true}}
	transaction.Transaction.Message.Instructions = []solanaParsedInstruction{
		{
			Program:   "spl-memo",
			ProgramID: "MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr",
			Parsed:    order.Reference,
		},
		{
			Program:   "system",
			ProgramID: "11111111111111111111111111111111",
			Parsed: map[string]any{
				"type": "transfer",
				"info": map[string]any{
					"source":      payer,
					"destination": order.DestinationWallet,
					"lamports":    float64(lamports),
				},
			},
		},
	}
	return transaction
}
