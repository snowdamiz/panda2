package billing

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

const (
	SolOrderStatusPending   = "pending"
	SolOrderStatusVerified  = "verified"
	SolOrderStatusExpired   = "expired"
	SolOrderStatusFailed    = "failed"
	SolOrderStatusActivated = "activated"

	SolTransactionStatusVerified = "verified"
	SolTransactionStatusFailed   = "failed"

	ActivationKeyStatusUnused   = "unused"
	ActivationKeyStatusConsumed = "consumed"
	ActivationKeyStatusExpired  = "expired"
	ActivationKeyStatusRevoked  = "revoked"
)

var (
	ErrSolPaymentOrderNotFound      = errors.New("sol payment order was not found")
	ErrSolPaymentOrderExpired       = errors.New("sol payment order expired")
	ErrSolPaymentOrderNotVerified   = errors.New("sol payment order is not verified")
	ErrSolPaymentOrderAlreadyActive = errors.New("sol payment order is already activated")
	ErrSolPaymentVerificationFailed = errors.New("sol payment verification failed")
	ErrActivationKeyAlreadyRevealed = errors.New("activation api key was already revealed")
	ErrActivationKeyInvalid         = errors.New("activation api key is invalid")
	ErrActivationKeyExpired         = errors.New("activation api key expired")
	ErrActivationKeyConsumed        = errors.New("activation api key was already consumed")
	ErrActivationKeyRevoked         = errors.New("activation api key was revoked")
	ErrSolanaTransactionUnavailable = errors.New("solana transaction is not available at the required commitment")
)

type CreateSolPaymentOrderRequest struct {
	GuildID            string
	BillingOwnerUserID string
	Plan               string
	SupportEmail       string
}

type SolPaymentOrderView struct {
	OrderID                      string    `json:"order_id"`
	GuildID                      string    `json:"guild_id"`
	BillingOwnerUserID           string    `json:"billing_owner_user_id,omitempty"`
	Plan                         string    `json:"plan"`
	DisplayName                  string    `json:"display_name"`
	ExpectedLamports             int64     `json:"expected_lamports"`
	AmountSOL                    string    `json:"amount_sol"`
	DestinationWallet            string    `json:"destination_wallet"`
	Reference                    string    `json:"reference"`
	Memo                         string    `json:"memo"`
	Cluster                      string    `json:"cluster"`
	ConfirmationThreshold        string    `json:"confirmation_threshold"`
	Status                       string    `json:"status"`
	PaymentURL                   string    `json:"payment_url"`
	VerifiedTransactionSignature string    `json:"verified_transaction_signature,omitempty"`
	ExpiresAt                    time.Time `json:"expires_at"`
	CreatedAt                    time.Time `json:"created_at"`
	UpdatedAt                    time.Time `json:"updated_at"`
}

type VerifySolPaymentRequest struct {
	OrderID   string
	Signature string
}

type SolPaymentVerificationResult struct {
	Order        SolPaymentOrderView `json:"order"`
	Verified     bool                `json:"verified"`
	FailureCode  string              `json:"failure_code,omitempty"`
	FailureError string              `json:"failure_error,omitempty"`
}

type ActivationKeyReveal struct {
	Order     SolPaymentOrderView `json:"order"`
	Key       string              `json:"api_key"`
	Prefix    string              `json:"prefix"`
	ExpiresAt time.Time           `json:"expires_at"`
}

type ActivateAPIKeyRequest struct {
	GuildID         string
	ActorUserID     string
	ActorIsOperator bool
	ActorCanClaim   bool
	APIKey          string
}

type ActivateAPIKeyResult struct {
	Entitlement Entitlement
}

type RevokeActivationAPIKeyRequest struct {
	PaymentOrderID  string
	ActorUserID     string
	ActorIsOperator bool
	Reason          string
}

type ActivationKeyRevocation struct {
	Order     SolPaymentOrderView
	Prefix    string
	RevokedAt time.Time
}

type SolanaRPCClient interface {
	GetTransaction(ctx context.Context, signature string, commitment string) (SolanaTransaction, error)
}

type HTTPSolanaRPCClient struct {
	endpoint string
	client   *http.Client
}

func NewHTTPSolanaRPCClient(endpoint string) *HTTPSolanaRPCClient {
	return &HTTPSolanaRPCClient{
		endpoint: strings.TrimSpace(endpoint),
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *HTTPSolanaRPCClient) GetTransaction(ctx context.Context, signature string, commitment string) (SolanaTransaction, error) {
	if c == nil || strings.TrimSpace(c.endpoint) == "" {
		return SolanaTransaction{}, ErrSolPaymentsNotConfigured
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "getTransaction",
		"params": []any{
			strings.TrimSpace(signature),
			map[string]any{
				"commitment":                     strings.TrimSpace(commitment),
				"encoding":                       "jsonParsed",
				"maxSupportedTransactionVersion": 0,
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return SolanaTransaction{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return SolanaTransaction{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return SolanaTransaction{}, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return SolanaTransaction{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SolanaTransaction{}, fmt.Errorf("solana rpc error (%d): %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var response struct {
		Result *SolanaTransaction `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return SolanaTransaction{}, fmt.Errorf("decode solana rpc response: %w", err)
	}
	if response.Error != nil {
		return SolanaTransaction{}, fmt.Errorf("solana rpc error (%d): %s", response.Error.Code, response.Error.Message)
	}
	if response.Result == nil {
		return SolanaTransaction{}, ErrSolanaTransactionUnavailable
	}
	return *response.Result, nil
}

type SolanaTransaction struct {
	Slot      uint64 `json:"slot"`
	BlockTime *int64 `json:"blockTime"`
	Meta      struct {
		Err any `json:"err"`
	} `json:"meta"`
	Transaction struct {
		Message struct {
			AccountKeys  []solanaAccountKey        `json:"accountKeys"`
			Instructions []solanaParsedInstruction `json:"instructions"`
		} `json:"message"`
		Signatures []string `json:"signatures"`
	} `json:"transaction"`
}

type solanaAccountKey struct {
	Pubkey string `json:"pubkey"`
	Signer bool   `json:"signer"`
}

func (k *solanaAccountKey) UnmarshalJSON(data []byte) error {
	var object struct {
		Pubkey string `json:"pubkey"`
		Signer bool   `json:"signer"`
	}
	if err := json.Unmarshal(data, &object); err == nil && object.Pubkey != "" {
		*k = solanaAccountKey{Pubkey: object.Pubkey, Signer: object.Signer}
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	*k = solanaAccountKey{Pubkey: text}
	return nil
}

type solanaParsedInstruction struct {
	Program   string `json:"program"`
	ProgramID string `json:"programId"`
	Parsed    any    `json:"parsed"`
}

func (s *Service) CreateSolPaymentOrder(ctx context.Context, request CreateSolPaymentOrderRequest) (SolPaymentOrderView, error) {
	if s == nil || s.repo == nil {
		return SolPaymentOrderView{}, ErrNoSubscription
	}
	if !s.solPaymentsConfigured() {
		return SolPaymentOrderView{}, ErrSolPaymentsNotConfigured
	}
	guildID := strings.TrimSpace(request.GuildID)
	if guildID == "" {
		return SolPaymentOrderView{}, fmt.Errorf("guild_id is required")
	}
	plan, ok := NormalizePlan(request.Plan)
	if !ok || plan == PlanTrial {
		return SolPaymentOrderView{}, ErrUnknownPlan
	}
	lamports := s.cfg.SolanaPlanLamports[plan]
	if lamports <= 0 {
		return SolPaymentOrderView{}, ErrSolPaymentsNotConfigured
	}
	now := s.currentTime()
	orderID, err := randomBase58(18)
	if err != nil {
		return SolPaymentOrderView{}, err
	}
	reference, err := randomBase58(32)
	if err != nil {
		return SolPaymentOrderView{}, err
	}
	order, err := s.repo.CreateSolPaymentOrder(ctx, store.SolPaymentOrder{
		OrderID:               "sol_" + orderID,
		GuildID:               guildID,
		BillingOwnerUserID:    strings.TrimSpace(request.BillingOwnerUserID),
		SupportEmail:          strings.TrimSpace(request.SupportEmail),
		Plan:                  plan,
		ExpectedLamports:      lamports,
		DestinationWallet:     s.cfg.SolanaTreasuryWallet,
		Reference:             reference,
		Status:                SolOrderStatusPending,
		Cluster:               s.cfg.SolanaCluster,
		ConfirmationThreshold: s.cfg.SolanaConfirmation,
		ExpiresAt:             now.Add(s.cfg.SolanaOrderExpiration),
		CreatedAt:             now,
		UpdatedAt:             now,
	})
	if err != nil {
		return SolPaymentOrderView{}, err
	}
	return s.solPaymentOrderView(order), nil
}

func (s *Service) GetSolPaymentOrder(ctx context.Context, orderID string) (SolPaymentOrderView, error) {
	if s == nil || s.repo == nil {
		return SolPaymentOrderView{}, ErrNoSubscription
	}
	order, ok, err := s.repo.GetSolPaymentOrder(ctx, orderID)
	if err != nil {
		return SolPaymentOrderView{}, err
	}
	if !ok {
		return SolPaymentOrderView{}, ErrSolPaymentOrderNotFound
	}
	order, err = s.expireOrderIfNeeded(ctx, order)
	if err != nil {
		return SolPaymentOrderView{}, err
	}
	return s.solPaymentOrderView(order), nil
}

func (s *Service) VerifySolPayment(ctx context.Context, request VerifySolPaymentRequest) (SolPaymentVerificationResult, error) {
	if s == nil || s.repo == nil {
		return SolPaymentVerificationResult{}, ErrNoSubscription
	}
	if !s.solPaymentsConfigured() || s.solana == nil {
		return SolPaymentVerificationResult{}, ErrSolPaymentsNotConfigured
	}
	signature := strings.TrimSpace(request.Signature)
	if signature == "" {
		return SolPaymentVerificationResult{}, fmt.Errorf("transaction signature is required")
	}
	order, ok, err := s.repo.GetSolPaymentOrder(ctx, request.OrderID)
	if err != nil {
		return SolPaymentVerificationResult{}, err
	}
	if !ok {
		return SolPaymentVerificationResult{}, ErrSolPaymentOrderNotFound
	}
	order, err = s.expireOrderIfNeeded(ctx, order)
	if err != nil {
		return SolPaymentVerificationResult{}, err
	}
	if order.Status == SolOrderStatusExpired {
		return SolPaymentVerificationResult{Order: s.solPaymentOrderView(order), FailureCode: "expired"}, ErrSolPaymentOrderExpired
	}
	if order.Status == SolOrderStatusActivated {
		return SolPaymentVerificationResult{Order: s.solPaymentOrderView(order), Verified: true}, nil
	}
	if order.Status == SolOrderStatusVerified && order.VerifiedTransactionSignature == signature {
		return SolPaymentVerificationResult{Order: s.solPaymentOrderView(order), Verified: true}, nil
	}

	transaction, err := s.solana.GetTransaction(ctx, signature, order.ConfirmationThreshold)
	if err != nil {
		code := "rpc_unavailable"
		if errors.Is(err, ErrSolanaTransactionUnavailable) {
			code = "pending_confirmation"
		}
		return SolPaymentVerificationResult{Order: s.solPaymentOrderView(order), FailureCode: code, FailureError: err.Error()}, ErrSolPaymentVerificationFailed
	}
	verified, err := verifyTransactionForOrder(transaction, order)
	if err != nil {
		_ = s.recordFailedSolVerification(ctx, order, signature, "verification_failed", err)
		return SolPaymentVerificationResult{Order: s.solPaymentOrderView(order), FailureCode: "verification_failed", FailureError: err.Error()}, ErrSolPaymentVerificationFailed
	}
	verified.Signature = signature
	verified.OrderID = order.OrderID
	verified.GuildID = order.GuildID
	verified.Status = SolTransactionStatusVerified
	verified.ConfirmationStatus = order.ConfirmationThreshold
	verified.RawPayload = MarshalRaw(transaction)

	err = s.repo.WithTransaction(ctx, func(repo *repository.BillingRepository) error {
		locked, ok, err := repo.GetSolPaymentOrderForUpdate(ctx, order.OrderID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSolPaymentOrderNotFound
		}
		if s.currentTime().After(locked.ExpiresAt) {
			return ErrSolPaymentOrderExpired
		}
		if locked.Status == SolOrderStatusVerified && locked.VerifiedTransactionSignature == signature {
			return nil
		}
		if locked.Status == SolOrderStatusActivated {
			return nil
		}
		inserted, err := repo.RecordSolPaymentTransaction(ctx, verified)
		if err != nil {
			return err
		}
		if !inserted {
			return fmt.Errorf("transaction signature has already been recorded")
		}
		now := s.currentTime()
		return repo.UpdateSolPaymentOrder(ctx, locked.OrderID, map[string]any{
			"status":                         SolOrderStatusVerified,
			"verified_transaction_signature": signature,
			"verified_at":                    &now,
		})
	})
	if err != nil {
		_ = s.recordFailedSolVerification(ctx, order, signature, "duplicate_or_stale", err)
		return SolPaymentVerificationResult{Order: s.solPaymentOrderView(order), FailureCode: "duplicate_or_stale", FailureError: err.Error()}, ErrSolPaymentVerificationFailed
	}
	updated, _, err := s.repo.GetSolPaymentOrder(ctx, order.OrderID)
	if err != nil {
		return SolPaymentVerificationResult{}, err
	}
	return SolPaymentVerificationResult{Order: s.solPaymentOrderView(updated), Verified: true}, nil
}

func (s *Service) RevealActivationKey(ctx context.Context, orderID string) (ActivationKeyReveal, error) {
	if s == nil || s.repo == nil {
		return ActivationKeyReveal{}, ErrNoSubscription
	}
	var rawKey string
	var savedKey store.ActivationAPIKey
	var updatedOrder store.SolPaymentOrder
	err := s.repo.WithTransaction(ctx, func(repo *repository.BillingRepository) error {
		order, ok, err := repo.GetSolPaymentOrderForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSolPaymentOrderNotFound
		}
		if s.currentTime().After(order.ExpiresAt) {
			return ErrSolPaymentOrderExpired
		}
		if order.Status != SolOrderStatusVerified {
			return ErrSolPaymentOrderNotVerified
		}
		if _, ok, err := repo.GetActivationAPIKeyByPaymentOrder(ctx, order.OrderID); err != nil {
			return err
		} else if ok {
			return ErrActivationKeyAlreadyRevealed
		}
		rawKey, err = newActivationAPIKey()
		if err != nil {
			return err
		}
		now := s.currentTime()
		keyID, err := randomBase58(18)
		if err != nil {
			return err
		}
		key := store.ActivationAPIKey{
			KeyID:          "act_" + keyID,
			KeyHash:        activationKeyHash(rawKey),
			KeyPrefix:      activationKeyPrefix(rawKey),
			PaymentOrderID: order.OrderID,
			GuildID:        order.GuildID,
			Plan:           order.Plan,
			Status:         ActivationKeyStatusUnused,
			ExpiresAt:      now.Add(s.cfg.SolanaActivationKeyTTL),
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		savedKey, err = repo.CreateActivationAPIKey(ctx, key)
		if err != nil {
			return err
		}
		if err := repo.UpdateSolPaymentOrder(ctx, order.OrderID, map[string]any{
			"activation_key_revealed_at": &now,
		}); err != nil {
			return err
		}
		updatedOrder = order
		updatedOrder.ActivationKeyRevealedAt = &now
		updatedOrder.UpdatedAt = now
		return nil
	})
	if err != nil {
		return ActivationKeyReveal{}, err
	}
	s.recordActivationKeyAudit(ctx, "billing.activation_key.created", firstNonEmpty(updatedOrder.BillingOwnerUserID, updatedOrder.GuildID), savedKey, updatedOrder, map[string]string{
		"event": "created",
	})
	s.recordActivationKeyAudit(ctx, "billing.activation_key.viewed", firstNonEmpty(updatedOrder.BillingOwnerUserID, updatedOrder.GuildID), savedKey, updatedOrder, map[string]string{
		"event": "viewed",
	})
	return ActivationKeyReveal{
		Order:     s.solPaymentOrderView(updatedOrder),
		Key:       rawKey,
		Prefix:    savedKey.KeyPrefix,
		ExpiresAt: savedKey.ExpiresAt,
	}, nil
}

func (s *Service) ActivateWithAPIKey(ctx context.Context, request ActivateAPIKeyRequest) (ActivateAPIKeyResult, error) {
	if s == nil || s.repo == nil {
		return ActivateAPIKeyResult{}, ErrNoSubscription
	}
	request.GuildID = strings.TrimSpace(request.GuildID)
	request.ActorUserID = strings.TrimSpace(request.ActorUserID)
	if request.GuildID == "" || strings.TrimSpace(request.APIKey) == "" {
		return ActivateAPIKeyResult{}, ErrActivationKeyInvalid
	}
	var subscription store.GuildSubscription
	var consumedKey store.ActivationAPIKey
	var consumedOrder store.SolPaymentOrder
	var expiredKey store.ActivationAPIKey
	err := s.repo.WithTransaction(ctx, func(repo *repository.BillingRepository) error {
		key, ok, err := repo.GetActivationAPIKeyByHashForUpdate(ctx, activationKeyHash(request.APIKey))
		if err != nil {
			return err
		}
		if !ok {
			return ErrActivationKeyInvalid
		}
		now := s.currentTime()
		if key.GuildID != request.GuildID {
			return ErrActivationKeyInvalid
		}
		switch key.Status {
		case ActivationKeyStatusConsumed:
			return ErrActivationKeyConsumed
		case ActivationKeyStatusRevoked:
			return ErrActivationKeyRevoked
		case ActivationKeyStatusExpired:
			return ErrActivationKeyExpired
		case ActivationKeyStatusUnused:
		default:
			return ErrActivationKeyInvalid
		}
		if key.RevokedAt != nil {
			return ErrActivationKeyRevoked
		}
		if !now.Before(key.ExpiresAt) {
			_ = repo.UpdateActivationAPIKey(ctx, key.KeyID, map[string]any{"status": ActivationKeyStatusExpired})
			expiredKey = key
			expiredKey.Status = ActivationKeyStatusExpired
			return ErrActivationKeyExpired
		}
		allowed, err := s.canManageBillingWithRepo(ctx, repo, request.GuildID, request.ActorUserID, request.ActorIsOperator, request.ActorCanClaim)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrBillingAccess
		}
		order, ok, err := repo.GetSolPaymentOrderForUpdate(ctx, key.PaymentOrderID)
		if err != nil {
			return err
		}
		if !ok || order.Status != SolOrderStatusVerified {
			return ErrSolPaymentOrderNotVerified
		}
		if order.GuildID != request.GuildID || order.Plan != key.Plan {
			return ErrActivationKeyInvalid
		}
		limits, ok := LimitsForPlan(order.Plan)
		if !ok || order.Plan == PlanTrial {
			return ErrUnknownPlan
		}
		account, err := repo.EnsureCustomerAccount(ctx, store.CustomerAccount{
			GuildID:            request.GuildID,
			BillingOwnerUserID: request.ActorUserID,
			SupportContact:     order.SupportEmail,
		})
		if err != nil {
			return err
		}
		periodStart := now
		periodEnd := periodStart.AddDate(0, 1, 0)
		saved, err := repo.UpsertSubscriptionWithSnapshot(ctx, store.GuildSubscription{
			GuildID:                request.GuildID,
			CustomerAccountID:      account.ID,
			Plan:                   order.Plan,
			Status:                 StatusActive,
			GraceState:             GraceActive,
			PaymentProvider:        ProviderSol,
			ExternalSubscriptionID: order.OrderID,
			ExternalEntitlementID:  order.VerifiedTransactionSignature,
			BillingOwnerUserID:     request.ActorUserID,
			CurrentPeriodStart:     periodStart,
			CurrentPeriodEnd:       periodEnd,
		}, snapshotForLimits(request.GuildID, 0, limits, StatusActive, GraceActive, now))
		if err != nil {
			return err
		}
		subscription = saved
		_, err = repo.RecordInvoicePaymentEvent(ctx, store.InvoicePaymentEvent{
			Provider:       ProviderSol,
			ExternalID:     firstNonEmpty(order.VerifiedTransactionSignature, order.OrderID),
			GuildID:        request.GuildID,
			SubscriptionID: saved.ID,
			AmountLamports: order.ExpectedLamports,
			Currency:       "sol",
			Status:         "paid",
			IdempotencyKey: "sol:activation:" + key.KeyID,
			RawPayload: MarshalRaw(map[string]any{
				"order_id":   order.OrderID,
				"signature":  order.VerifiedTransactionSignature,
				"plan":       order.Plan,
				"lamports":   order.ExpectedLamports,
				"key_prefix": key.KeyPrefix,
			}),
			CreatedAt: now,
		})
		if err != nil {
			return err
		}
		consumedAt := now
		if err := repo.UpdateActivationAPIKey(ctx, key.KeyID, map[string]any{
			"status":                      ActivationKeyStatusConsumed,
			"consumed_at":                 &consumedAt,
			"consumed_by_discord_user_id": request.ActorUserID,
		}); err != nil {
			return err
		}
		consumedKey = key
		consumedKey.Status = ActivationKeyStatusConsumed
		consumedKey.ConsumedAt = &consumedAt
		consumedKey.ConsumedByDiscordUserID = request.ActorUserID
		consumedOrder = order
		if err := repo.UpdateSolPaymentOrder(ctx, order.OrderID, map[string]any{
			"status":       SolOrderStatusActivated,
			"activated_at": &consumedAt,
		}); err != nil {
			return err
		}
		consumedOrder.Status = SolOrderStatusActivated
		consumedOrder.ActivatedAt = &consumedAt
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrActivationKeyExpired) && expiredKey.KeyID != "" {
			s.recordActivationKeyAudit(ctx, "billing.activation_key.expired", request.ActorUserID, expiredKey, store.SolPaymentOrder{OrderID: expiredKey.PaymentOrderID, GuildID: expiredKey.GuildID, Plan: expiredKey.Plan}, map[string]string{
				"event": "expired",
			})
		}
		return ActivateAPIKeyResult{}, err
	}
	s.recordActivationKeyAudit(ctx, "billing.activation_key.consumed", request.ActorUserID, consumedKey, consumedOrder, map[string]string{
		"event": "consumed",
	})
	entitlement, err := s.entitlementFromSubscription(ctx, subscription)
	if err != nil {
		return ActivateAPIKeyResult{}, err
	}
	return ActivateAPIKeyResult{Entitlement: entitlement}, nil
}

func (s *Service) RevokeActivationAPIKey(ctx context.Context, request RevokeActivationAPIKeyRequest) (ActivationKeyRevocation, error) {
	if s == nil || s.repo == nil {
		return ActivationKeyRevocation{}, ErrNoSubscription
	}
	if !request.ActorIsOperator {
		return ActivationKeyRevocation{}, ErrBillingAccess
	}
	orderID := strings.TrimSpace(request.PaymentOrderID)
	if orderID == "" {
		return ActivationKeyRevocation{}, ErrActivationKeyInvalid
	}
	request.ActorUserID = strings.TrimSpace(request.ActorUserID)
	var revokedKey store.ActivationAPIKey
	var order store.SolPaymentOrder
	var revokedAt time.Time
	err := s.repo.WithTransaction(ctx, func(repo *repository.BillingRepository) error {
		key, ok, err := repo.GetActivationAPIKeyByPaymentOrderForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrActivationKeyInvalid
		}
		switch key.Status {
		case ActivationKeyStatusConsumed:
			return ErrActivationKeyConsumed
		case ActivationKeyStatusRevoked:
			return ErrActivationKeyRevoked
		case ActivationKeyStatusExpired:
			return ErrActivationKeyExpired
		case ActivationKeyStatusUnused:
		default:
			return ErrActivationKeyInvalid
		}
		if key.ConsumedAt != nil {
			return ErrActivationKeyConsumed
		}
		if key.RevokedAt != nil {
			return ErrActivationKeyRevoked
		}
		now := s.currentTime()
		if !now.Before(key.ExpiresAt) {
			_ = repo.UpdateActivationAPIKey(ctx, key.KeyID, map[string]any{"status": ActivationKeyStatusExpired})
			return ErrActivationKeyExpired
		}
		loadedOrder, ok, err := repo.GetSolPaymentOrderForUpdate(ctx, key.PaymentOrderID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSolPaymentOrderNotFound
		}
		revokedAt = now
		if err := repo.UpdateActivationAPIKey(ctx, key.KeyID, map[string]any{
			"status":     ActivationKeyStatusRevoked,
			"revoked_at": &revokedAt,
		}); err != nil {
			return err
		}
		revokedKey = key
		revokedKey.Status = ActivationKeyStatusRevoked
		revokedKey.RevokedAt = &revokedAt
		order = loadedOrder
		return nil
	})
	if err != nil {
		return ActivationKeyRevocation{}, err
	}
	s.recordActivationKeyAudit(ctx, "billing.activation_key.revoked", request.ActorUserID, revokedKey, order, map[string]string{
		"event":  "revoked",
		"reason": truncateError(strings.TrimSpace(request.Reason)),
	})
	return ActivationKeyRevocation{
		Order:     s.solPaymentOrderView(order),
		Prefix:    revokedKey.KeyPrefix,
		RevokedAt: revokedAt,
	}, nil
}

func (s *Service) solPaymentsConfigured() bool {
	return s != nil &&
		strings.TrimSpace(s.cfg.SolanaRPCURL) != "" &&
		strings.TrimSpace(s.cfg.SolanaTreasuryWallet) != "" &&
		strings.TrimSpace(s.cfg.SolanaCluster) != "" &&
		strings.TrimSpace(s.cfg.SolanaConfirmation) != ""
}

func (s *Service) expireOrderIfNeeded(ctx context.Context, order store.SolPaymentOrder) (store.SolPaymentOrder, error) {
	if order.Status != SolOrderStatusPending && order.Status != SolOrderStatusFailed {
		return order, nil
	}
	if s.currentTime().Before(order.ExpiresAt) {
		return order, nil
	}
	if err := s.repo.UpdateSolPaymentOrder(ctx, order.OrderID, map[string]any{"status": SolOrderStatusExpired}); err != nil {
		return store.SolPaymentOrder{}, err
	}
	order.Status = SolOrderStatusExpired
	order.UpdatedAt = s.currentTime()
	return order, nil
}

func (s *Service) recordFailedSolVerification(ctx context.Context, order store.SolPaymentOrder, signature string, code string, cause error) error {
	if strings.TrimSpace(signature) == "" {
		return nil
	}
	errorMessage := code
	if cause != nil {
		errorMessage = cause.Error()
	}
	_, err := s.repo.RecordSolPaymentTransaction(ctx, store.SolPaymentTransaction{
		Signature:    signature,
		OrderID:      order.OrderID,
		GuildID:      order.GuildID,
		Reference:    order.Reference,
		Status:       SolTransactionStatusFailed,
		ErrorMessage: truncateError(errorMessage),
		RawPayload:   "{}",
		CreatedAt:    s.currentTime(),
		UpdatedAt:    s.currentTime(),
	})
	if err != nil {
		return err
	}
	return s.repo.UpdateSolPaymentOrder(ctx, order.OrderID, map[string]any{"status": SolOrderStatusFailed})
}

func (s *Service) recordActivationKeyAudit(ctx context.Context, action string, actorID string, key store.ActivationAPIKey, order store.SolPaymentOrder, extra map[string]string) {
	if s == nil || s.audit == nil || key.KeyID == "" {
		return
	}
	metadata := map[string]string{
		"key_id":         key.KeyID,
		"key_prefix":     key.KeyPrefix,
		"order_id":       firstNonEmpty(order.OrderID, key.PaymentOrderID),
		"plan":           firstNonEmpty(order.Plan, key.Plan),
		"key_status":     key.Status,
		"payment_status": order.Status,
	}
	for name, value := range extra {
		if strings.TrimSpace(value) != "" {
			metadata[name] = strings.TrimSpace(value)
		}
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		data = []byte("{}")
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    firstNonEmpty(order.GuildID, key.GuildID),
		ActorID:    strings.TrimSpace(actorID),
		Action:     action,
		TargetType: "activation_api_key",
		TargetID:   key.KeyID,
		Metadata:   string(data),
		CreatedAt:  s.currentTime(),
	})
}

func (s *Service) canManageBillingWithRepo(ctx context.Context, repo *repository.BillingRepository, guildID, userID string, operator bool, canClaim bool) (bool, error) {
	if operator {
		return true, nil
	}
	guildID = strings.TrimSpace(guildID)
	userID = strings.TrimSpace(userID)
	if guildID == "" || userID == "" {
		return false, nil
	}
	account, accountOK, err := repo.GetCustomerAccountByGuild(ctx, guildID)
	if err != nil {
		return false, err
	}
	if accountOK && strings.TrimSpace(account.BillingOwnerUserID) == userID {
		return true, nil
	}
	subscription, subscriptionOK, err := repo.GetSubscriptionByGuild(ctx, guildID)
	if err != nil {
		return false, err
	}
	if subscriptionOK && strings.TrimSpace(subscription.BillingOwnerUserID) == userID {
		return true, nil
	}
	if !canClaim {
		return false, nil
	}
	if accountOK && strings.TrimSpace(account.BillingOwnerUserID) != "" {
		return false, nil
	}
	return !subscriptionOK || strings.TrimSpace(subscription.BillingOwnerUserID) == "", nil
}

func (s *Service) solPaymentOrderView(order store.SolPaymentOrder) SolPaymentOrderView {
	limits, _ := LimitsForPlan(order.Plan)
	return SolPaymentOrderView{
		OrderID:                      order.OrderID,
		GuildID:                      order.GuildID,
		BillingOwnerUserID:           order.BillingOwnerUserID,
		Plan:                         order.Plan,
		DisplayName:                  firstNonEmpty(limits.DisplayName, order.Plan),
		ExpectedLamports:             order.ExpectedLamports,
		AmountSOL:                    formatLamports(order.ExpectedLamports),
		DestinationWallet:            order.DestinationWallet,
		Reference:                    order.Reference,
		Memo:                         order.Reference,
		Cluster:                      order.Cluster,
		ConfirmationThreshold:        order.ConfirmationThreshold,
		Status:                       order.Status,
		PaymentURL:                   solanaPayURL(order),
		VerifiedTransactionSignature: order.VerifiedTransactionSignature,
		ExpiresAt:                    order.ExpiresAt.UTC(),
		CreatedAt:                    order.CreatedAt.UTC(),
		UpdatedAt:                    order.UpdatedAt.UTC(),
	}
}

func verifyTransactionForOrder(transaction SolanaTransaction, order store.SolPaymentOrder) (store.SolPaymentTransaction, error) {
	if transaction.Meta.Err != nil {
		return store.SolPaymentTransaction{}, fmt.Errorf("transaction meta contains an execution error")
	}
	var matchingTransfers []store.SolPaymentTransaction
	var memoMatched bool
	var tokenInstruction bool
	for _, instruction := range transaction.Transaction.Message.Instructions {
		if isTokenInstruction(instruction) {
			tokenInstruction = true
			continue
		}
		if instructionMemo(instruction) == order.Reference {
			memoMatched = true
			continue
		}
		transfer, ok := parsedSystemTransfer(instruction)
		if !ok {
			continue
		}
		if transfer.DestinationWallet != order.DestinationWallet {
			continue
		}
		transfer.Reference = order.Reference
		matchingTransfers = append(matchingTransfers, transfer)
	}
	if tokenInstruction {
		return store.SolPaymentTransaction{}, fmt.Errorf("token instructions are not accepted for SOL-only orders")
	}
	if !memoMatched {
		return store.SolPaymentTransaction{}, fmt.Errorf("payment memo/reference did not match the order")
	}
	if len(matchingTransfers) != 1 {
		return store.SolPaymentTransaction{}, fmt.Errorf("expected exactly one native SOL transfer to the treasury wallet, got %d", len(matchingTransfers))
	}
	transfer := matchingTransfers[0]
	if transfer.AmountLamports < order.ExpectedLamports {
		return store.SolPaymentTransaction{}, fmt.Errorf("native SOL transfer underpaid order: got %d lamports, expected %d", transfer.AmountLamports, order.ExpectedLamports)
	}
	if transfer.PayerWallet == "" {
		transfer.PayerWallet = firstSigner(transaction)
	}
	return transfer, nil
}

func parsedSystemTransfer(instruction solanaParsedInstruction) (store.SolPaymentTransaction, bool) {
	if !strings.EqualFold(instruction.Program, "system") && instruction.ProgramID != "11111111111111111111111111111111" {
		return store.SolPaymentTransaction{}, false
	}
	parsed, ok := instruction.Parsed.(map[string]any)
	if !ok {
		return store.SolPaymentTransaction{}, false
	}
	if !strings.EqualFold(stringValue(parsed["type"]), "transfer") {
		return store.SolPaymentTransaction{}, false
	}
	info, ok := parsed["info"].(map[string]any)
	if !ok {
		return store.SolPaymentTransaction{}, false
	}
	lamports, ok := int64Value(info["lamports"])
	if !ok {
		return store.SolPaymentTransaction{}, false
	}
	return store.SolPaymentTransaction{
		PayerWallet:       stringValue(info["source"]),
		DestinationWallet: stringValue(info["destination"]),
		AmountLamports:    lamports,
	}, true
}

func instructionMemo(instruction solanaParsedInstruction) string {
	if !strings.EqualFold(instruction.Program, "spl-memo") && !isMemoProgramID(instruction.ProgramID) {
		return ""
	}
	switch parsed := instruction.Parsed.(type) {
	case string:
		return strings.TrimSpace(parsed)
	case map[string]any:
		if value := strings.TrimSpace(stringValue(parsed["memo"])); value != "" {
			return value
		}
		if info, ok := parsed["info"].(map[string]any); ok {
			return strings.TrimSpace(stringValue(info["memo"]))
		}
	}
	return ""
}

func isMemoProgramID(programID string) bool {
	switch strings.TrimSpace(programID) {
	case "MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr", "Memo1UhkJRfHyvLMcVucJwxXeuD728EqVDDwQDxFMNo":
		return true
	default:
		return false
	}
}

func isTokenInstruction(instruction solanaParsedInstruction) bool {
	switch strings.TrimSpace(instruction.ProgramID) {
	case "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", "TokenzQdBNbLqP5VEZPjS5xJ1hbEGj2pPPQ1MRvfV5z":
		return true
	}
	program := strings.ToLower(strings.TrimSpace(instruction.Program))
	return program == "spl-token" || program == "spl-token-2022"
}

func firstSigner(transaction SolanaTransaction) string {
	for _, key := range transaction.Transaction.Message.AccountKeys {
		if key.Signer {
			return strings.TrimSpace(key.Pubkey)
		}
	}
	return ""
}

func solanaPayURL(order store.SolPaymentOrder) string {
	if strings.TrimSpace(order.DestinationWallet) == "" {
		return ""
	}
	values := url.Values{}
	values.Set("amount", formatLamports(order.ExpectedLamports))
	values.Set("reference", order.Reference)
	values.Set("memo", order.Reference)
	values.Set("label", "Panda")
	values.Set("message", fmt.Sprintf("Panda %s plan for Discord server %s", order.Plan, order.GuildID))
	return "solana:" + order.DestinationWallet + "?" + values.Encode()
}

func formatLamports(lamports int64) string {
	if lamports <= 0 {
		return "0"
	}
	whole := lamports / 1_000_000_000
	fraction := lamports % 1_000_000_000
	if fraction == 0 {
		return fmt.Sprintf("%d", whole)
	}
	text := fmt.Sprintf("%d.%09d", whole, fraction)
	return strings.TrimRight(text, "0")
}

func normalizePlanLamports(values map[string]int64) map[string]int64 {
	result := map[string]int64{}
	for plan, lamports := range values {
		plan, ok := NormalizePlan(plan)
		if !ok || plan == PlanTrial || lamports <= 0 {
			continue
		}
		result[plan] = lamports
	}
	return result
}

func activationKeyHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

func activationKeyPrefix(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 18 {
		return value
	}
	return value[:18]
}

func newActivationAPIKey() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(bytes)
	return "panda_sol_" + secret, nil
}

func randomBase58(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return encodeBase58(bytes), nil
}

func encodeBase58(input []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	if len(input) == 0 {
		return ""
	}
	digits := []byte{0}
	for _, value := range input {
		carry := int(value)
		for index := range digits {
			carry += int(digits[index]) << 8
			digits[index] = byte(carry % 58)
			carry /= 58
		}
		for carry > 0 {
			digits = append(digits, byte(carry%58))
			carry /= 58
		}
	}
	for _, value := range input {
		if value != 0 {
			break
		}
		digits = append(digits, 0)
	}
	for left, right := 0, len(digits)-1; left < right; left, right = left+1, right-1 {
		digits[left], digits[right] = digits[right], digits[left]
	}
	var builder strings.Builder
	builder.Grow(len(digits))
	for _, digit := range digits {
		builder.WriteByte(alphabet[digit])
	}
	return builder.String()
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func int64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if typed < 0 || typed != float64(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func truncateError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 512 {
		return value
	}
	return value[:512]
}
