package billing

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	storepkg "github.com/sn0w/panda2/internal/store"
)

func TestEnsureTrialMetersCreditsAndDeniesOverBalance(t *testing.T) {
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
	if entitlement.Pack.Pack != PackTrial || entitlement.Status != StatusTrialing || !entitlement.CanUsePaidFeatures || entitlement.ReadOnly {
		t.Fatalf("unexpected trial entitlement: %+v", entitlement)
	}
	if entitlement.AvailableCredits != entitlement.Pack.Credits || entitlement.ReservedCredits != 0 {
		t.Fatalf("expected full trial credit balance, got %+v", entitlement)
	}

	reservation, err := service.BeginUsage(ctx, "guild-1", MetricAIResponse, entitlement.Pack.Credits/4-1)
	if err != nil {
		t.Fatalf("BeginUsage near trial balance: %v", err)
	}
	if reservation.ID == "" {
		t.Fatal("expected reservation id")
	}
	if err := service.CommitUsage(ctx, reservation); err != nil {
		t.Fatalf("CommitUsage: %v", err)
	}

	_, err = service.BeginUsage(ctx, "guild-1", MetricAIResponse, 2)
	var creditErr CreditError
	if !errors.As(err, &creditErr) {
		t.Fatalf("expected CreditError after consuming trial credits, got %T %v", err, err)
	}
	if creditErr.Action != ActionAssistantModelRound || creditErr.Used != entitlement.Pack.Credits-4 || creditErr.Limit != entitlement.Pack.Credits || creditErr.RequiredCredits != 8 || creditErr.AvailableCredits != 4 {
		t.Fatalf("unexpected credit error: %+v", creditErr)
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
		BillingOwnerUserID: "owner-1",
		Plan:               PackPlus,
		SupportEmail:       "owner@example.com",
	})
	if err != nil {
		t.Fatalf("CreateSolPaymentOrder: %v", err)
	}
	if order.ExpectedLamports != 49_000_000 || order.DestinationWallet != "treasury-wallet" || order.Reference == "" || order.PaymentURL != "" {
		t.Fatalf("unexpected order view: %+v", order)
	}
	if order.GuildID != "" {
		t.Fatalf("account-level payment order should not be guild-bound before activation: %+v", order)
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
	if !strings.HasPrefix(reveal.Key, "panda_act_") || reveal.Prefix == "" {
		t.Fatalf("unexpected activation key reveal: %+v", reveal)
	}
	if reveal.Order.GuildID != "" {
		t.Fatalf("activation key reveal should stay account-level until Discord activation: %+v", reveal.Order)
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
	if activated.Entitlement.Pack.Pack != PackPlus || activated.Entitlement.PaymentProvider != ProviderSol || !activated.Entitlement.CanUsePaidFeatures {
		t.Fatalf("unexpected activated entitlement: %+v", activated.Entitlement)
	}
	if activated.Entitlement.AvailableCredits < packDefinitions[PackPlus].Credits {
		t.Fatalf("expected plus pack credits to be granted, got %+v", activated.Entitlement)
	}
	activatedOrder, ok, err := repository.NewBillingRepository(database.DB).GetBillingOrder(ctx, order.OrderID)
	if err != nil || !ok {
		t.Fatalf("load activated order: ok=%v err=%v", ok, err)
	}
	if activatedOrder.GuildID != "guild-1" || activatedOrder.Status != SolOrderStatusActivated {
		t.Fatalf("activation should bind order to guild: %+v", activatedOrder)
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
		Plan:    PackStarter,
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
		Plan:               PackPro,
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

func TestCouponCreateDuplicateAndInvalidOrderCreation(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	expires := now.Add(time.Hour)
	created, err := service.CreateCoupon(ctx, CreateCouponRequest{
		ActorUserID:      "owner-1",
		ActorIsOwner:     true,
		Plan:             PackPlus,
		DiscountLamports: 10_000_000,
		Code:             "PLUS10",
		MaxRedemptions:   1,
		ExpiresAt:        &expires,
		Note:             "launch",
	})
	if err != nil {
		t.Fatalf("CreateCoupon: %v", err)
	}
	if created.Code != "PLUS10" || created.Coupon.CodePrefix != "PLUS10" || created.Coupon.DiscountLamports != 10_000_000 {
		t.Fatalf("unexpected created coupon: %+v", created)
	}

	var stored storepkg.BillingCoupon
	if err := database.DB.Where("coupon_id = ?", created.Coupon.CouponID).First(&stored).Error; err != nil {
		t.Fatalf("load coupon: %v", err)
	}
	if stored.CodeHash == "PLUS10" || stored.CodeHash == "" {
		t.Fatalf("coupon code should be hashed at rest: %+v", stored)
	}
	if _, err := service.CreateCoupon(ctx, CreateCouponRequest{
		ActorUserID:      "owner-1",
		ActorIsOwner:     true,
		Plan:             PackPlus,
		DiscountLamports: 5_000_000,
		Code:             "PLUS10",
	}); !errors.Is(err, ErrCouponDuplicate) {
		t.Fatalf("expected duplicate coupon error, got %v", err)
	}
	if _, err := service.CreateCoupon(ctx, CreateCouponRequest{
		ActorUserID:      "user-1",
		Plan:             PackPlus,
		DiscountLamports: 5_000_000,
	}); !errors.Is(err, ErrBillingAccess) {
		t.Fatalf("expected owner-only create error, got %v", err)
	}
	if _, err := service.CreateSolPaymentOrder(ctx, CreateSolPaymentOrderRequest{
		GuildID:    "guild-1",
		Plan:       PackPro,
		CouponCode: created.Code,
	}); !errors.Is(err, ErrCouponPackMismatch) {
		t.Fatalf("expected wrong-pack coupon error, got %v", err)
	}

	revoked, err := service.RevokeCoupon(ctx, RevokeCouponRequest{ActorUserID: "owner-1", ActorIsOwner: true, CouponID: created.Coupon.CouponID})
	if err != nil {
		t.Fatalf("RevokeCoupon: %v", err)
	}
	if revoked.Status != CouponStatusRevoked {
		t.Fatalf("expected revoked coupon, got %+v", revoked)
	}
	if _, err := service.CreateSolPaymentOrder(ctx, CreateSolPaymentOrderRequest{
		GuildID:    "guild-1",
		Plan:       PackPlus,
		CouponCode: created.Code,
	}); !errors.Is(err, ErrCouponRevoked) {
		t.Fatalf("expected revoked coupon error, got %v", err)
	}
}

func TestLimitedCouponReservationAndExpirationRelease(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	coupon, err := service.CreateCoupon(ctx, CreateCouponRequest{
		ActorUserID:      "owner-1",
		ActorIsOwner:     true,
		Plan:             PackStarter,
		DiscountLamports: 1_000_000,
		Code:             "ONEUSE",
		MaxRedemptions:   1,
	})
	if err != nil {
		t.Fatalf("CreateCoupon: %v", err)
	}
	order, err := service.CreateSolPaymentOrder(ctx, CreateSolPaymentOrderRequest{
		GuildID:    "guild-1",
		Plan:       PackStarter,
		CouponCode: coupon.Code,
	})
	if err != nil {
		t.Fatalf("CreateSolPaymentOrder with coupon: %v", err)
	}
	if order.ListLamports != 19_000_000 || order.DiscountLamports != 1_000_000 || order.DueLamports != 18_000_000 || order.CouponPrefix != "ONEUSE" {
		t.Fatalf("unexpected discounted order: %+v", order)
	}
	if _, err := service.CreateSolPaymentOrder(ctx, CreateSolPaymentOrderRequest{
		GuildID:    "guild-2",
		Plan:       PackStarter,
		CouponCode: coupon.Code,
	}); !errors.Is(err, ErrCouponExhausted) {
		t.Fatalf("expected exhausted coupon error, got %v", err)
	}

	now = now.Add(2 * time.Hour)
	service.SetClock(func() time.Time { return now })
	if _, err := service.GetSolPaymentOrder(ctx, order.OrderID); err != nil {
		t.Fatalf("GetSolPaymentOrder after expiration: %v", err)
	}
	redemptions, err := service.ListCoupons(ctx, ListCouponsRequest{ActorUserID: "owner-1", ActorIsOwner: true})
	if err != nil {
		t.Fatalf("ListCoupons: %v", err)
	}
	if len(redemptions) != 1 || redemptions[0].Released != 1 || redemptions[0].Pending != 0 {
		t.Fatalf("expected released pending redemption, got %+v", redemptions)
	}
}

func TestPaidCouponOrderVerifiesDueLamports(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	coupon, err := service.CreateCoupon(ctx, CreateCouponRequest{
		ActorUserID:      "owner-1",
		ActorIsOwner:     true,
		Plan:             PackPlus,
		DiscountLamports: 10_000_000,
		Code:             "PLUS-DUE",
	})
	if err != nil {
		t.Fatalf("CreateCoupon: %v", err)
	}
	order, err := service.CreateSolPaymentOrder(ctx, CreateSolPaymentOrderRequest{
		GuildID:    "guild-1",
		Plan:       PackPlus,
		CouponCode: coupon.Code,
	})
	if err != nil {
		t.Fatalf("CreateSolPaymentOrder: %v", err)
	}
	if order.DueLamports != 39_000_000 {
		t.Fatalf("expected discounted due lamports, got %+v", order)
	}
	service.WithSolanaRPCClient(fakeSolanaRPCClient{transaction: verifiedTransaction(order, "payer-wallet", 38_999_999)})
	if _, err := service.VerifySolPayment(ctx, VerifySolPaymentRequest{OrderID: order.OrderID, Signature: "sig-underpay"}); !errors.Is(err, ErrSolPaymentVerificationFailed) {
		t.Fatalf("expected underpay failure against due amount, got %v", err)
	}

	service.WithSolanaRPCClient(fakeSolanaRPCClient{transaction: verifiedTransaction(order, "payer-wallet", 39_000_000)})
	result, err := service.VerifySolPayment(ctx, VerifySolPaymentRequest{OrderID: order.OrderID, Signature: "sig-discounted"})
	if err != nil {
		t.Fatalf("VerifySolPayment discounted due: %v result=%+v", err, result)
	}
	if !result.Verified || result.Order.DueLamports != 39_000_000 {
		t.Fatalf("unexpected discounted verification: %+v", result)
	}
}

func TestServerPreparedSolTransactionAndSubmission(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	service.cfg.SolanaTreasuryWallet = "2gRg3JMJkJkWb85fh3RqNCQgbmGYRpa1Gk5o84Y84ve1"
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	order, err := service.CreateSolPaymentOrder(ctx, CreateSolPaymentOrderRequest{
		GuildID: "guild-1",
		Plan:    PackStarter,
	})
	if err != nil {
		t.Fatalf("CreateSolPaymentOrder: %v", err)
	}
	rpc := fakeSolanaRPCClient{
		latestBlockhash: SolanaLatestBlockhash{
			Blockhash:            "11111111111111111111111111111111",
			LastValidBlockHeight: 12345,
		},
		sentSignature: "sig-server-submitted",
		transaction:   verifiedTransaction(order, "2gRg3JMJkJkWb85fh3RqNCQgbmGYRpa1Gk5o84Y84ve1", 19_000_000),
	}
	service.WithSolanaRPCClient(rpc)

	prepared, err := service.PrepareSolPaymentTransaction(ctx, PrepareSolPaymentTransactionRequest{
		OrderID:     order.OrderID,
		PayerWallet: "2gRg3JMJkJkWb85fh3RqNCQgbmGYRpa1Gk5o84Y84ve1",
	})
	if err != nil {
		t.Fatalf("PrepareSolPaymentTransaction: %v", err)
	}
	if prepared.Transaction == "" || prepared.LastValidBlockHeight != 12345 || prepared.Order.PaymentURL != "" {
		t.Fatalf("unexpected prepared transaction: %+v", prepared)
	}
	if _, err := base64.StdEncoding.DecodeString(prepared.Transaction); err != nil {
		t.Fatalf("prepared transaction should be base64: %v", err)
	}

	result, err := service.SubmitSolPaymentTransaction(ctx, SubmitSolPaymentTransactionRequest{
		OrderID:           order.OrderID,
		SignedTransaction: prepared.Transaction,
	})
	if err != nil {
		t.Fatalf("SubmitSolPaymentTransaction: %v result=%+v", err, result)
	}
	if !result.Verified || result.SubmittedSignature != "sig-server-submitted" {
		t.Fatalf("unexpected submit result: %+v", result)
	}
}

func TestFreeCouponOrderRevealsAndActivatesWithoutSolana(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	service.cfg.SolanaRPCURL = ""
	service.cfg.SolanaTreasuryWallet = ""
	service.solana = nil

	coupon, err := service.CreateCoupon(ctx, CreateCouponRequest{
		ActorUserID:      "owner-1",
		ActorIsOwner:     true,
		Plan:             PackBusiness,
		DiscountLamports: 999_000_000,
		Code:             "COMP-BIZ",
	})
	if err != nil {
		t.Fatalf("CreateCoupon: %v", err)
	}
	order, err := service.CreateSolPaymentOrder(ctx, CreateSolPaymentOrderRequest{
		BillingOwnerUserID: "owner-1",
		Plan:               PackBusiness,
		CouponCode:         coupon.Code,
	})
	if err != nil {
		t.Fatalf("CreateSolPaymentOrder free coupon: %v", err)
	}
	if order.DueLamports != 0 || order.Status != SolOrderStatusVerified || order.DestinationWallet != "" || order.PaymentURL != "" {
		t.Fatalf("unexpected free coupon order: %+v", order)
	}
	reveal, err := service.RevealActivationKey(ctx, order.OrderID)
	if err != nil {
		t.Fatalf("RevealActivationKey free coupon: %v", err)
	}
	if reveal.Key == "" {
		t.Fatal("expected activation key for free coupon")
	}
	activated, err := service.ActivateWithAPIKey(ctx, ActivateAPIKeyRequest{
		GuildID:       "guild-1",
		ActorUserID:   "owner-1",
		ActorCanClaim: true,
		APIKey:        reveal.Key,
	})
	if err != nil {
		t.Fatalf("ActivateWithAPIKey free coupon: %v", err)
	}
	if activated.Entitlement.Pack.Pack != PackBusiness || activated.Entitlement.PaymentProvider != ProviderCoupon || !activated.Entitlement.CanUsePaidFeatures {
		t.Fatalf("unexpected free coupon entitlement: %+v", activated.Entitlement)
	}
	var event storepkg.InvoicePaymentEvent
	if err := database.DB.Where("provider = ? AND external_id = ?", ProviderCoupon, order.OrderID).First(&event).Error; err != nil {
		t.Fatalf("load coupon payment event: %v", err)
	}
	if event.AmountLamports != 0 || event.Currency != "coupon" || event.Status != "comped" || !strings.Contains(event.RawPayload, `"coupon_prefix":"COMP-BIZ"`) {
		t.Fatalf("unexpected coupon payment event: %+v", event)
	}
	for _, action := range []string{"billing.coupon.redemption.reserved", "billing.coupon.redemption.consumed", "billing.coupon.free_activation_key_revealed"} {
		var count int64
		if err := database.DB.Model(&storepkg.AuditEvent{}).Where("action = ?", action).Count(&count).Error; err != nil {
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
			PackStarter:  19_000_000,
			PackPlus:     49_000_000,
			PackPro:      99_000_000,
			PackBusiness: 249_000_000,
		},
	}).WithAuditRecorder(repository.NewAuditRepository(database.DB))
	return service, database
}

type fakeSolanaRPCClient struct {
	transaction     SolanaTransaction
	latestBlockhash SolanaLatestBlockhash
	sentSignature   string
	err             error
	sendErr         error
}

func (f fakeSolanaRPCClient) GetTransaction(context.Context, string, string) (SolanaTransaction, error) {
	if f.err != nil {
		return SolanaTransaction{}, f.err
	}
	return f.transaction, nil
}

func (f fakeSolanaRPCClient) GetLatestBlockhash(context.Context, string) (SolanaLatestBlockhash, error) {
	if f.err != nil {
		return SolanaLatestBlockhash{}, f.err
	}
	if f.latestBlockhash.Blockhash == "" {
		return SolanaLatestBlockhash{Blockhash: "11111111111111111111111111111111", LastValidBlockHeight: 1}, nil
	}
	return f.latestBlockhash, nil
}

func (f fakeSolanaRPCClient) SendTransaction(context.Context, string, string) (string, error) {
	if f.sendErr != nil {
		return "", f.sendErr
	}
	if f.sentSignature == "" {
		return "sig-submitted", nil
	}
	return f.sentSignature, nil
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

func TestAdminOverviewReportsTrialAndUsage(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })

	overview, err := service.AdminOverview(ctx, "guild-1")
	if err != nil {
		t.Fatalf("AdminOverview before credit account: %v", err)
	}
	if overview.HasCreditAccount {
		t.Fatalf("expected no credit account, got %+v", overview)
	}

	if _, err := service.EnsureTrial(ctx, TrialSeed{GuildID: "guild-1", BillingOwnerUserID: "owner-1", Email: "owner@example.com", AuthorizedAt: now}); err != nil {
		t.Fatalf("EnsureTrial: %v", err)
	}

	overview, err = service.AdminOverview(ctx, "guild-1")
	if err != nil {
		t.Fatalf("AdminOverview: %v", err)
	}
	if !overview.HasCreditAccount || overview.Pack != PackTrial || overview.StoredStatus != StatusTrialing {
		t.Fatalf("unexpected overview: %+v", overview)
	}
	if overview.Email != "owner@example.com" || overview.BillingOwnerUserID != "owner-1" {
		t.Fatalf("expected customer account details, got %+v", overview)
	}
	if overview.Limits.Credits != packDefinitions[PackTrial].Credits || overview.AvailableCredits != packDefinitions[PackTrial].Credits {
		t.Fatalf("unexpected limits: %+v", overview.Limits)
	}
}

func TestAdminSetCreditAccountOverridesPackAndStatus(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	if _, err := service.EnsureTrial(ctx, TrialSeed{GuildID: "guild-1", BillingOwnerUserID: "owner-1", AuthorizedAt: now}); err != nil {
		t.Fatalf("EnsureTrial: %v", err)
	}

	periodEnd := now.AddDate(0, 1, 0)
	cancel := true
	overview, err := service.AdminSetCreditAccount(ctx, AdminSetCreditAccountRequest{
		GuildID:           "guild-1",
		ActorUserID:       "treasury_wallet:abc",
		Pack:              PackPro,
		Status:            StatusActive,
		PeriodEnd:         &periodEnd,
		CancelAtPeriodEnd: &cancel,
	})
	if err != nil {
		t.Fatalf("AdminSetCreditAccount: %v", err)
	}
	if overview.Pack != PackPro || overview.StoredStatus != StatusActive {
		t.Fatalf("expected pro/active, got %+v", overview)
	}
	if overview.PaymentProvider != ProviderManual {
		t.Fatalf("expected manual provider, got %q", overview.PaymentProvider)
	}
	if overview.Credits != packDefinitions[PackPro].Credits || overview.AvailableCredits < packDefinitions[PackPro].Credits {
		t.Fatalf("unexpected pro credit grant fields: %+v", overview)
	}

	// The override should be persisted and resolvable as paid entitlement.
	entitlement, err := service.Resolve(ctx, "guild-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if entitlement.Pack.Pack != PackPro || !entitlement.CanUsePaidFeatures {
		t.Fatalf("unexpected resolved entitlement: %+v", entitlement)
	}

	// Suspending should remove paid access without changing the pack.
	overview, err = service.AdminSetCreditAccount(ctx, AdminSetCreditAccountRequest{
		GuildID: "guild-1",
		Status:  StatusSuspended,
	})
	if err != nil {
		t.Fatalf("AdminSetCreditAccount suspend: %v", err)
	}
	if overview.Pack != PackPro || overview.StoredStatus != StatusSuspended || !overview.ReadOnly {
		t.Fatalf("expected suspended read-only pro pack, got %+v", overview)
	}
}

func TestAdminSetCreditAccountRejectsUnknownPack(t *testing.T) {
	ctx := context.Background()
	service, database := newBillingTestService(t)
	defer database.Close()

	if _, err := service.AdminSetCreditAccount(ctx, AdminSetCreditAccountRequest{GuildID: "guild-1", Pack: "enterprise"}); err == nil {
		t.Fatal("expected error for unknown pack")
	}
}
