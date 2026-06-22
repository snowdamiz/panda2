package billing

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
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

	CouponStatusActive  = "active"
	CouponStatusRevoked = "revoked"
	CouponStatusExpired = "expired"

	CouponRedemptionStatusPending  = "pending"
	CouponRedemptionStatusConsumed = "consumed"
	CouponRedemptionStatusReleased = "released"
)

var (
	ErrSolPaymentOrderNotFound      = errors.New("sol payment order was not found")
	ErrSolPaymentOrderExpired       = errors.New("sol payment order expired")
	ErrSolPaymentOrderNotVerified   = errors.New("sol payment order is not verified")
	ErrSolPaymentOrderAlreadyActive = errors.New("sol payment order is already activated")
	ErrSolPaymentNotRequired        = errors.New("sol payment is not required for this order")
	ErrSolPaymentVerificationFailed = errors.New("sol payment verification failed")
	ErrActivationKeyAlreadyRevealed = errors.New("activation api key was already revealed")
	ErrActivationKeyInvalid         = errors.New("activation api key is invalid")
	ErrActivationKeyExpired         = errors.New("activation api key expired")
	ErrActivationKeyConsumed        = errors.New("activation api key was already consumed")
	ErrActivationKeyRevoked         = errors.New("activation api key was revoked")
	ErrSolanaTransactionUnavailable = errors.New("solana transaction is not available at the required commitment")
	ErrCouponInvalid                = errors.New("coupon code is invalid")
	ErrCouponExpired                = errors.New("coupon expired")
	ErrCouponRevoked                = errors.New("coupon revoked")
	ErrCouponPlanMismatch           = errors.New("coupon does not apply to the selected plan")
	ErrCouponExhausted              = errors.New("coupon redemptions are exhausted")
	ErrCouponDuplicate              = errors.New("coupon code already exists")
	ErrCouponNotFound               = errors.New("coupon was not found")
	ErrCouponAmbiguous              = errors.New("coupon prefix matches multiple coupons")
)

type CreateSolPaymentOrderRequest struct {
	GuildID            string
	BillingOwnerUserID string
	Plan               string
	SupportEmail       string
	CouponCode         string
}

type SolPaymentOrderView struct {
	OrderID                      string    `json:"order_id"`
	GuildID                      string    `json:"guild_id"`
	BillingOwnerUserID           string    `json:"billing_owner_user_id,omitempty"`
	Plan                         string    `json:"plan"`
	DisplayName                  string    `json:"display_name"`
	ListLamports                 int64     `json:"list_lamports"`
	DiscountLamports             int64     `json:"discount_lamports"`
	DueLamports                  int64     `json:"due_lamports"`
	ExpectedLamports             int64     `json:"expected_lamports"`
	AmountSOL                    string    `json:"amount_sol"`
	CouponPrefix                 string    `json:"coupon_prefix,omitempty"`
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
	Order              SolPaymentOrderView `json:"order"`
	Verified           bool                `json:"verified"`
	SubmittedSignature string              `json:"submitted_signature,omitempty"`
	FailureCode        string              `json:"failure_code,omitempty"`
	FailureError       string              `json:"failure_error,omitempty"`
}

type PrepareSolPaymentTransactionRequest struct {
	OrderID     string
	PayerWallet string
}

type SolPaymentTransactionPreparation struct {
	Order                SolPaymentOrderView `json:"order"`
	Transaction          string              `json:"transaction"`
	PayerWallet          string              `json:"payer_wallet"`
	LastValidBlockHeight uint64              `json:"last_valid_block_height"`
}

type SubmitSolPaymentTransactionRequest struct {
	OrderID           string
	SignedTransaction string
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

type BillingQuoteRequest struct {
	Plan       string
	CouponCode string
}

type BillingQuote struct {
	Plan             string
	DisplayName      string
	ListLamports     int64
	DiscountLamports int64
	DueLamports      int64
	CouponID         string
	CouponPrefix     string
}

type CreateCouponRequest struct {
	ActorUserID      string
	ActorIsOwner     bool
	Plan             string
	DiscountLamports int64
	Code             string
	MaxRedemptions   int
	ExpiresAt        *time.Time
	Note             string
}

type CouponCreateResult struct {
	Coupon CouponView
	Code   string
}

type ListCouponsRequest struct {
	ActorUserID  string
	ActorIsOwner bool
}

type RevokeCouponRequest struct {
	ActorUserID  string
	ActorIsOwner bool
	CouponID     string
	Prefix       string
}

type CouponView struct {
	CouponID         string     `json:"coupon_id"`
	CodePrefix       string     `json:"code_prefix"`
	Plan             string     `json:"plan"`
	DisplayName      string     `json:"display_name"`
	DiscountLamports int64      `json:"discount_lamports"`
	MaxRedemptions   int        `json:"max_redemptions"`
	Status           string     `json:"status"`
	OwnerNote        string     `json:"owner_note,omitempty"`
	CreatedByUserID  string     `json:"created_by_user_id"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	Pending          int        `json:"pending"`
	Consumed         int        `json:"consumed"`
	Released         int        `json:"released"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type SolanaRPCClient interface {
	GetTransaction(ctx context.Context, signature string, commitment string) (SolanaTransaction, error)
	GetLatestBlockhash(ctx context.Context, commitment string) (SolanaLatestBlockhash, error)
	SendTransaction(ctx context.Context, signedTransaction string, commitment string) (string, error)
}

type HTTPSolanaRPCClient struct {
	endpoint string
	client   *http.Client
}

type SolanaLatestBlockhash struct {
	Blockhash            string
	LastValidBlockHeight uint64
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

func (c *HTTPSolanaRPCClient) GetLatestBlockhash(ctx context.Context, commitment string) (SolanaLatestBlockhash, error) {
	if c == nil || strings.TrimSpace(c.endpoint) == "" {
		return SolanaLatestBlockhash{}, ErrSolPaymentsNotConfigured
	}
	var response struct {
		Result *struct {
			Value struct {
				Blockhash            string `json:"blockhash"`
				LastValidBlockHeight uint64 `json:"lastValidBlockHeight"`
			} `json:"value"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := c.rpc(ctx, "getLatestBlockhash", []any{map[string]any{"commitment": strings.TrimSpace(commitment)}}, &response); err != nil {
		return SolanaLatestBlockhash{}, err
	}
	if response.Error != nil {
		return SolanaLatestBlockhash{}, fmt.Errorf("solana rpc error (%d): %s", response.Error.Code, response.Error.Message)
	}
	if response.Result == nil || strings.TrimSpace(response.Result.Value.Blockhash) == "" {
		return SolanaLatestBlockhash{}, ErrSolanaTransactionUnavailable
	}
	return SolanaLatestBlockhash{
		Blockhash:            strings.TrimSpace(response.Result.Value.Blockhash),
		LastValidBlockHeight: response.Result.Value.LastValidBlockHeight,
	}, nil
}

func (c *HTTPSolanaRPCClient) SendTransaction(ctx context.Context, signedTransaction string, commitment string) (string, error) {
	if c == nil || strings.TrimSpace(c.endpoint) == "" {
		return "", ErrSolPaymentsNotConfigured
	}
	var response struct {
		Result string `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := c.rpc(ctx, "sendTransaction", []any{
		strings.TrimSpace(signedTransaction),
		map[string]any{
			"encoding":            "base64",
			"skipPreflight":       false,
			"preflightCommitment": strings.TrimSpace(commitment),
			"maxRetries":          3,
		},
	}, &response); err != nil {
		return "", err
	}
	if response.Error != nil {
		return "", fmt.Errorf("solana rpc error (%d): %s", response.Error.Code, response.Error.Message)
	}
	if strings.TrimSpace(response.Result) == "" {
		return "", ErrSolanaTransactionUnavailable
	}
	return strings.TrimSpace(response.Result), nil
}

func (c *HTTPSolanaRPCClient) rpc(ctx context.Context, method string, params []any, response any) error {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("solana rpc error (%d): %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	if err := json.Unmarshal(responseBody, response); err != nil {
		return fmt.Errorf("decode solana rpc response: %w", err)
	}
	return nil
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

func (s *Service) QuoteBillingOrder(ctx context.Context, request BillingQuoteRequest) (BillingQuote, error) {
	if s == nil || s.repo == nil {
		return BillingQuote{}, ErrNoSubscription
	}
	plan, ok := NormalizePlan(request.Plan)
	if !ok || plan == PlanTrial {
		return BillingQuote{}, ErrUnknownPlan
	}
	now := s.currentTime()
	var quote BillingQuote
	err := s.repo.WithTransaction(ctx, func(repo *repository.BillingRepository) error {
		var err error
		quote, err = s.quoteBillingOrderWithRepo(ctx, repo, plan, request.CouponCode, now)
		return err
	})
	return quote, err
}

func (s *Service) CreateSolPaymentOrder(ctx context.Context, request CreateSolPaymentOrderRequest) (SolPaymentOrderView, error) {
	if s == nil || s.repo == nil {
		return SolPaymentOrderView{}, ErrNoSubscription
	}
	guildID := strings.TrimSpace(request.GuildID)
	if guildID == "" {
		return SolPaymentOrderView{}, fmt.Errorf("guild_id is required")
	}
	plan, ok := NormalizePlan(request.Plan)
	if !ok || plan == PlanTrial {
		return SolPaymentOrderView{}, ErrUnknownPlan
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
	var order store.BillingOrder
	var redemption store.BillingCouponRedemption
	err = s.repo.WithTransaction(ctx, func(repo *repository.BillingRepository) error {
		quote, err := s.quoteBillingOrderWithRepo(ctx, repo, plan, request.CouponCode, now)
		if err != nil {
			return err
		}
		if quote.DueLamports > 0 && !s.solPaymentsConfigured() {
			return ErrSolPaymentsNotConfigured
		}
		provider := ProviderSol
		status := SolOrderStatusPending
		var verifiedAt *time.Time
		if quote.DueLamports == 0 {
			provider = ProviderCoupon
			status = SolOrderStatusVerified
			verifiedAt = &now
		}
		destinationWallet := ""
		cluster := ""
		confirmation := ""
		if quote.DueLamports > 0 {
			destinationWallet = s.cfg.SolanaTreasuryWallet
			cluster = s.cfg.SolanaCluster
			confirmation = s.cfg.SolanaConfirmation
		}
		order, err = repo.CreateBillingOrder(ctx, store.BillingOrder{
			OrderID:               "ord_" + orderID,
			GuildID:               guildID,
			BillingOwnerUserID:    strings.TrimSpace(request.BillingOwnerUserID),
			SupportEmail:          strings.TrimSpace(request.SupportEmail),
			Plan:                  quote.Plan,
			Provider:              provider,
			ListLamports:          quote.ListLamports,
			DiscountLamports:      quote.DiscountLamports,
			DueLamports:           quote.DueLamports,
			CouponID:              quote.CouponID,
			CouponPrefix:          quote.CouponPrefix,
			DestinationWallet:     destinationWallet,
			Reference:             reference,
			Status:                status,
			Cluster:               cluster,
			ConfirmationThreshold: confirmation,
			VerifiedAt:            verifiedAt,
			ExpiresAt:             now.Add(s.cfg.SolanaOrderExpiration),
			CreatedAt:             now,
			UpdatedAt:             now,
		})
		if err != nil {
			return err
		}
		if quote.CouponID == "" {
			return nil
		}
		redemptionID, err := randomBase58(18)
		if err != nil {
			return err
		}
		redemption, err = repo.CreateCouponRedemption(ctx, store.BillingCouponRedemption{
			RedemptionID:       "red_" + redemptionID,
			CouponID:           quote.CouponID,
			OrderID:            order.OrderID,
			GuildID:            guildID,
			BillingOwnerUserID: strings.TrimSpace(request.BillingOwnerUserID),
			Plan:               quote.Plan,
			ListLamports:       quote.ListLamports,
			DiscountLamports:   quote.DiscountLamports,
			DueLamports:        quote.DueLamports,
			Status:             CouponRedemptionStatusPending,
			ExpiresAt:          order.ExpiresAt,
			CreatedAt:          now,
			UpdatedAt:          now,
		})
		return err
	})
	if err != nil {
		return SolPaymentOrderView{}, err
	}
	if redemption.RedemptionID != "" {
		s.recordCouponAudit(ctx, "billing.coupon.redemption.reserved", firstNonEmpty(order.BillingOwnerUserID, order.GuildID), store.BillingCoupon{CouponID: order.CouponID, CodePrefix: order.CouponPrefix, Plan: order.Plan}, map[string]any{
			"redemption_id":     redemption.RedemptionID,
			"order_id":          order.OrderID,
			"guild_id":          order.GuildID,
			"list_lamports":     order.ListLamports,
			"discount_lamports": order.DiscountLamports,
			"due_lamports":      order.DueLamports,
		})
	}
	return s.solPaymentOrderView(order), nil
}

func (s *Service) CreateCoupon(ctx context.Context, request CreateCouponRequest) (CouponCreateResult, error) {
	if s == nil || s.repo == nil {
		return CouponCreateResult{}, ErrNoSubscription
	}
	if !request.ActorIsOwner {
		return CouponCreateResult{}, ErrBillingAccess
	}
	plan, ok := NormalizePlan(request.Plan)
	if !ok || plan == PlanTrial {
		return CouponCreateResult{}, ErrUnknownPlan
	}
	if request.DiscountLamports <= 0 {
		return CouponCreateResult{}, fmt.Errorf("discount_lamports must be greater than zero")
	}
	if request.MaxRedemptions < 0 {
		return CouponCreateResult{}, fmt.Errorf("max_redemptions cannot be negative")
	}
	now := s.currentTime()
	if request.ExpiresAt != nil && !request.ExpiresAt.After(now) {
		return CouponCreateResult{}, ErrCouponExpired
	}
	rawCode := strings.TrimSpace(request.Code)
	if rawCode == "" {
		generated, err := newCouponCode()
		if err != nil {
			return CouponCreateResult{}, err
		}
		rawCode = generated
	}
	couponID, err := randomBase58(18)
	if err != nil {
		return CouponCreateResult{}, err
	}
	coupon, err := s.repo.CreateBillingCoupon(ctx, store.BillingCoupon{
		CouponID:         "cpn_" + couponID,
		CodeHash:         couponCodeHash(rawCode),
		CodePrefix:       couponCodePrefix(rawCode),
		Plan:             plan,
		DiscountLamports: request.DiscountLamports,
		MaxRedemptions:   request.MaxRedemptions,
		Status:           CouponStatusActive,
		OwnerNote:        request.Note,
		CreatedByUserID:  request.ActorUserID,
		ExpiresAt:        request.ExpiresAt,
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if repository.IsUniqueConstraintError(err) {
		return CouponCreateResult{}, ErrCouponDuplicate
	}
	if err != nil {
		return CouponCreateResult{}, err
	}
	view := s.couponView(coupon, repository.CouponRedemptionCounts{})
	s.recordCouponAudit(ctx, "billing.coupon.created", request.ActorUserID, coupon, map[string]any{
		"discount_lamports": coupon.DiscountLamports,
		"max_redemptions":   coupon.MaxRedemptions,
	})
	return CouponCreateResult{Coupon: view, Code: rawCode}, nil
}

func (s *Service) ListCoupons(ctx context.Context, request ListCouponsRequest) ([]CouponView, error) {
	if s == nil || s.repo == nil {
		return nil, ErrNoSubscription
	}
	if !request.ActorIsOwner {
		return nil, ErrBillingAccess
	}
	coupons, err := s.repo.ListBillingCoupons(ctx)
	if err != nil {
		return nil, err
	}
	couponIDs := make([]string, 0, len(coupons))
	for _, coupon := range coupons {
		couponIDs = append(couponIDs, coupon.CouponID)
	}
	counts, err := s.repo.CouponRedemptionCountsForCoupons(ctx, couponIDs)
	if err != nil {
		return nil, err
	}
	views := make([]CouponView, 0, len(coupons))
	now := s.currentTime()
	for _, coupon := range coupons {
		effective := s.effectiveCouponStatus(coupon, now)
		if effective.Status != coupon.Status {
			if err := s.repo.UpdateBillingCoupon(ctx, effective.CouponID, map[string]any{"status": effective.Status}); err != nil {
				return nil, err
			}
		}
		views = append(views, s.couponView(effective, counts[coupon.CouponID]))
	}
	return views, nil
}

func (s *Service) RevokeCoupon(ctx context.Context, request RevokeCouponRequest) (CouponView, error) {
	if s == nil || s.repo == nil {
		return CouponView{}, ErrNoSubscription
	}
	if !request.ActorIsOwner {
		return CouponView{}, ErrBillingAccess
	}
	couponID := strings.TrimSpace(request.CouponID)
	prefix := strings.TrimSpace(request.Prefix)
	if couponID == "" && prefix == "" {
		return CouponView{}, ErrCouponNotFound
	}
	now := s.currentTime()
	var coupon store.BillingCoupon
	var counts repository.CouponRedemptionCounts
	err := s.repo.WithTransaction(ctx, func(repo *repository.BillingRepository) error {
		var ok bool
		var err error
		if couponID != "" {
			coupon, ok, err = repo.GetBillingCouponByIDForUpdate(ctx, couponID)
			if err != nil {
				return err
			}
			if !ok {
				return ErrCouponNotFound
			}
		} else {
			matches, err := repo.FindBillingCouponsByPrefixForUpdate(ctx, prefix)
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				return ErrCouponNotFound
			}
			if len(matches) > 1 {
				return ErrCouponAmbiguous
			}
			coupon = matches[0]
		}
		if coupon.Status == CouponStatusRevoked || coupon.RevokedAt != nil {
			return ErrCouponRevoked
		}
		revokedAt := now
		if err := repo.UpdateBillingCoupon(ctx, coupon.CouponID, map[string]any{
			"status":     CouponStatusRevoked,
			"revoked_at": &revokedAt,
		}); err != nil {
			return err
		}
		coupon.Status = CouponStatusRevoked
		coupon.RevokedAt = &revokedAt
		coupon.UpdatedAt = now
		counts, err = repo.CouponRedemptionCounts(ctx, coupon.CouponID)
		return err
	})
	if err != nil {
		return CouponView{}, err
	}
	s.recordCouponAudit(ctx, "billing.coupon.revoked", request.ActorUserID, coupon, map[string]any{
		"pending_redemptions":  counts.Pending,
		"consumed_redemptions": counts.Consumed,
	})
	return s.couponView(coupon, counts), nil
}

func (s *Service) quoteBillingOrderWithRepo(ctx context.Context, repo *repository.BillingRepository, plan string, couponCode string, now time.Time) (BillingQuote, error) {
	limits, ok := LimitsForPlan(plan)
	if !ok || plan == PlanTrial {
		return BillingQuote{}, ErrUnknownPlan
	}
	listLamports := s.cfg.SolanaPlanLamports[plan]
	if listLamports <= 0 {
		return BillingQuote{}, ErrSolPaymentsNotConfigured
	}
	quote := BillingQuote{
		Plan:         plan,
		DisplayName:  limits.DisplayName,
		ListLamports: listLamports,
		DueLamports:  listLamports,
	}
	couponCode = strings.TrimSpace(couponCode)
	if couponCode == "" {
		return quote, nil
	}
	coupon, ok, err := repo.GetBillingCouponByCodeHashForUpdate(ctx, couponCodeHash(couponCode))
	if err != nil {
		return BillingQuote{}, err
	}
	if !ok {
		return BillingQuote{}, ErrCouponInvalid
	}
	if coupon.Status == CouponStatusRevoked || coupon.RevokedAt != nil {
		return BillingQuote{}, ErrCouponRevoked
	}
	if coupon.Status == CouponStatusExpired || (coupon.ExpiresAt != nil && !now.Before(*coupon.ExpiresAt)) {
		if coupon.Status != CouponStatusExpired {
			_ = repo.UpdateBillingCoupon(ctx, coupon.CouponID, map[string]any{"status": CouponStatusExpired})
		}
		return BillingQuote{}, ErrCouponExpired
	}
	if coupon.Status != CouponStatusActive {
		return BillingQuote{}, ErrCouponInvalid
	}
	if coupon.Plan != plan {
		return BillingQuote{}, ErrCouponPlanMismatch
	}
	counts, err := repo.CouponRedemptionCounts(ctx, coupon.CouponID)
	if err != nil {
		return BillingQuote{}, err
	}
	if coupon.MaxRedemptions > 0 && counts.Pending+counts.Consumed >= coupon.MaxRedemptions {
		return BillingQuote{}, ErrCouponExhausted
	}
	discount := coupon.DiscountLamports
	if discount > listLamports {
		discount = listLamports
	}
	quote.DiscountLamports = discount
	quote.DueLamports = listLamports - discount
	quote.CouponID = coupon.CouponID
	quote.CouponPrefix = coupon.CodePrefix
	return quote, nil
}

func (s *Service) GetSolPaymentOrder(ctx context.Context, orderID string) (SolPaymentOrderView, error) {
	if s == nil || s.repo == nil {
		return SolPaymentOrderView{}, ErrNoSubscription
	}
	order, ok, err := s.repo.GetBillingOrder(ctx, orderID)
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

func (s *Service) PrepareSolPaymentTransaction(ctx context.Context, request PrepareSolPaymentTransactionRequest) (SolPaymentTransactionPreparation, error) {
	if s == nil || s.repo == nil {
		return SolPaymentTransactionPreparation{}, ErrNoSubscription
	}
	if !s.solPaymentsConfigured() || s.solana == nil {
		return SolPaymentTransactionPreparation{}, ErrSolPaymentsNotConfigured
	}
	payerWallet := strings.TrimSpace(request.PayerWallet)
	if payerWallet == "" {
		return SolPaymentTransactionPreparation{}, fmt.Errorf("payer_wallet is required")
	}
	order, ok, err := s.repo.GetBillingOrder(ctx, request.OrderID)
	if err != nil {
		return SolPaymentTransactionPreparation{}, err
	}
	if !ok {
		return SolPaymentTransactionPreparation{}, ErrSolPaymentOrderNotFound
	}
	order, err = s.expireOrderIfNeeded(ctx, order)
	if err != nil {
		return SolPaymentTransactionPreparation{}, err
	}
	if order.Status == SolOrderStatusExpired {
		return SolPaymentTransactionPreparation{}, ErrSolPaymentOrderExpired
	}
	if order.DueLamports <= 0 {
		return SolPaymentTransactionPreparation{}, ErrSolPaymentNotRequired
	}
	if order.Status == SolOrderStatusActivated {
		return SolPaymentTransactionPreparation{}, ErrSolPaymentOrderAlreadyActive
	}
	if order.Status == SolOrderStatusVerified {
		return SolPaymentTransactionPreparation{}, ErrSolPaymentOrderAlreadyActive
	}
	latest, err := s.solana.GetLatestBlockhash(ctx, order.ConfirmationThreshold)
	if err != nil {
		return SolPaymentTransactionPreparation{}, err
	}
	transaction, err := buildSolPaymentTransactionBase64(order, payerWallet, latest.Blockhash)
	if err != nil {
		return SolPaymentTransactionPreparation{}, err
	}
	return SolPaymentTransactionPreparation{
		Order:                s.solPaymentOrderView(order),
		Transaction:          transaction,
		PayerWallet:          payerWallet,
		LastValidBlockHeight: latest.LastValidBlockHeight,
	}, nil
}

func (s *Service) SubmitSolPaymentTransaction(ctx context.Context, request SubmitSolPaymentTransactionRequest) (SolPaymentVerificationResult, error) {
	if s == nil || s.repo == nil {
		return SolPaymentVerificationResult{}, ErrNoSubscription
	}
	if !s.solPaymentsConfigured() || s.solana == nil {
		return SolPaymentVerificationResult{}, ErrSolPaymentsNotConfigured
	}
	signedTransaction := strings.TrimSpace(request.SignedTransaction)
	if signedTransaction == "" {
		return SolPaymentVerificationResult{}, fmt.Errorf("signed_transaction is required")
	}
	if _, err := base64.StdEncoding.DecodeString(signedTransaction); err != nil {
		return SolPaymentVerificationResult{}, fmt.Errorf("signed_transaction must be base64")
	}
	order, ok, err := s.repo.GetBillingOrder(ctx, request.OrderID)
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
	if order.DueLamports <= 0 {
		return SolPaymentVerificationResult{Order: s.solPaymentOrderView(order)}, ErrSolPaymentNotRequired
	}
	signature, err := s.solana.SendTransaction(ctx, signedTransaction, order.ConfirmationThreshold)
	if err != nil {
		return SolPaymentVerificationResult{Order: s.solPaymentOrderView(order), FailureCode: "rpc_unavailable", FailureError: err.Error()}, ErrSolPaymentVerificationFailed
	}
	result, err := s.VerifySolPayment(ctx, VerifySolPaymentRequest{OrderID: order.OrderID, Signature: signature})
	result.SubmittedSignature = signature
	if err != nil {
		return result, err
	}
	return result, nil
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
	order, ok, err := s.repo.GetBillingOrder(ctx, request.OrderID)
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
		locked, ok, err := repo.GetBillingOrderForUpdate(ctx, order.OrderID)
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
		return repo.UpdateBillingOrder(ctx, locked.OrderID, map[string]any{
			"status":                         SolOrderStatusVerified,
			"verified_transaction_signature": signature,
			"verified_at":                    &now,
		})
	})
	if err != nil {
		_ = s.recordFailedSolVerification(ctx, order, signature, "duplicate_or_stale", err)
		return SolPaymentVerificationResult{Order: s.solPaymentOrderView(order), FailureCode: "duplicate_or_stale", FailureError: err.Error()}, ErrSolPaymentVerificationFailed
	}
	updated, _, err := s.repo.GetBillingOrder(ctx, order.OrderID)
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
	var updatedOrder store.BillingOrder
	var consumedRedemption store.BillingCouponRedemption
	err := s.repo.WithTransaction(ctx, func(repo *repository.BillingRepository) error {
		order, ok, err := repo.GetBillingOrderForUpdate(ctx, orderID)
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
			BillingOrderID: order.OrderID,
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
		if order.CouponID != "" {
			redemption, ok, err := repo.GetCouponRedemptionByOrderForUpdate(ctx, order.OrderID)
			if err != nil {
				return err
			}
			if !ok || redemption.Status != CouponRedemptionStatusPending {
				return ErrCouponInvalid
			}
			if err := repo.UpdateCouponRedemption(ctx, redemption.RedemptionID, map[string]any{
				"status":      CouponRedemptionStatusConsumed,
				"consumed_at": &now,
			}); err != nil {
				return err
			}
			consumedRedemption = redemption
			consumedRedemption.Status = CouponRedemptionStatusConsumed
			consumedRedemption.ConsumedAt = &now
			consumedRedemption.UpdatedAt = now
		}
		if err := repo.UpdateBillingOrder(ctx, order.OrderID, map[string]any{
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
	if consumedRedemption.RedemptionID != "" {
		s.recordCouponAudit(ctx, "billing.coupon.redemption.consumed", firstNonEmpty(updatedOrder.BillingOwnerUserID, updatedOrder.GuildID), store.BillingCoupon{CouponID: updatedOrder.CouponID, CodePrefix: updatedOrder.CouponPrefix, Plan: updatedOrder.Plan}, map[string]any{
			"redemption_id":     consumedRedemption.RedemptionID,
			"order_id":          updatedOrder.OrderID,
			"guild_id":          updatedOrder.GuildID,
			"list_lamports":     updatedOrder.ListLamports,
			"discount_lamports": updatedOrder.DiscountLamports,
			"due_lamports":      updatedOrder.DueLamports,
		})
	}
	if updatedOrder.DueLamports == 0 && updatedOrder.CouponID != "" {
		s.recordActivationKeyAudit(ctx, "billing.coupon.free_activation_key_revealed", firstNonEmpty(updatedOrder.BillingOwnerUserID, updatedOrder.GuildID), savedKey, updatedOrder, map[string]string{
			"event":         "free_coupon_reveal",
			"coupon_prefix": updatedOrder.CouponPrefix,
		})
	}
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
	var consumedOrder store.BillingOrder
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
		order, ok, err := repo.GetBillingOrderForUpdate(ctx, key.BillingOrderID)
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
		paymentProvider := ProviderSol
		paymentStatus := "paid"
		paymentCurrency := "sol"
		paymentExternalID := firstNonEmpty(order.VerifiedTransactionSignature, order.OrderID)
		paymentIDPrefix := "sol"
		if order.DueLamports == 0 {
			paymentProvider = ProviderCoupon
			paymentStatus = "comped"
			paymentCurrency = "coupon"
			paymentExternalID = order.OrderID
			paymentIDPrefix = "coupon"
		}
		saved, err := repo.UpsertSubscriptionWithSnapshot(ctx, store.GuildSubscription{
			GuildID:                request.GuildID,
			CustomerAccountID:      account.ID,
			Plan:                   order.Plan,
			Status:                 StatusActive,
			GraceState:             GraceActive,
			PaymentProvider:        paymentProvider,
			ExternalSubscriptionID: order.OrderID,
			ExternalEntitlementID:  firstNonEmpty(order.VerifiedTransactionSignature, order.CouponPrefix, order.OrderID),
			BillingOwnerUserID:     request.ActorUserID,
			CurrentPeriodStart:     periodStart,
			CurrentPeriodEnd:       periodEnd,
		}, snapshotForLimits(request.GuildID, 0, limits, StatusActive, GraceActive, now))
		if err != nil {
			return err
		}
		subscription = saved
		_, err = repo.RecordInvoicePaymentEvent(ctx, store.InvoicePaymentEvent{
			Provider:       paymentProvider,
			ExternalID:     paymentExternalID,
			GuildID:        request.GuildID,
			SubscriptionID: saved.ID,
			AmountLamports: order.DueLamports,
			Currency:       paymentCurrency,
			Status:         paymentStatus,
			IdempotencyKey: paymentIDPrefix + ":activation:" + key.KeyID,
			RawPayload: MarshalRaw(map[string]any{
				"order_id":           order.OrderID,
				"signature":          order.VerifiedTransactionSignature,
				"plan":               order.Plan,
				"list_lamports":      order.ListLamports,
				"discount_lamports":  order.DiscountLamports,
				"due_lamports":       order.DueLamports,
				"coupon_id":          order.CouponID,
				"coupon_prefix":      order.CouponPrefix,
				"activation_key_id":  key.KeyID,
				"activation_prefix":  key.KeyPrefix,
				"payment_provider":   paymentProvider,
				"payment_event_type": paymentStatus,
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
		if err := repo.UpdateBillingOrder(ctx, order.OrderID, map[string]any{
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
			s.recordActivationKeyAudit(ctx, "billing.activation_key.expired", request.ActorUserID, expiredKey, store.BillingOrder{OrderID: expiredKey.BillingOrderID, GuildID: expiredKey.GuildID, Plan: expiredKey.Plan}, map[string]string{
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
	var order store.BillingOrder
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
		loadedOrder, ok, err := repo.GetBillingOrderForUpdate(ctx, key.BillingOrderID)
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

func (s *Service) expireOrderIfNeeded(ctx context.Context, order store.BillingOrder) (store.BillingOrder, error) {
	if order.Status != SolOrderStatusPending && order.Status != SolOrderStatusFailed {
		return order, nil
	}
	if s.currentTime().Before(order.ExpiresAt) {
		return order, nil
	}
	now := s.currentTime()
	err := s.repo.WithTransaction(ctx, func(repo *repository.BillingRepository) error {
		locked, ok, err := repo.GetBillingOrderForUpdate(ctx, order.OrderID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSolPaymentOrderNotFound
		}
		if locked.Status != SolOrderStatusPending && locked.Status != SolOrderStatusFailed {
			order = locked
			return nil
		}
		if now.Before(locked.ExpiresAt) {
			order = locked
			return nil
		}
		if locked.CouponID != "" {
			redemption, ok, err := repo.GetCouponRedemptionByOrderForUpdate(ctx, locked.OrderID)
			if err != nil {
				return err
			}
			if ok && redemption.Status == CouponRedemptionStatusPending {
				if err := repo.UpdateCouponRedemption(ctx, redemption.RedemptionID, map[string]any{
					"status":      CouponRedemptionStatusReleased,
					"released_at": &now,
				}); err != nil {
					return err
				}
			}
		}
		if err := repo.UpdateBillingOrder(ctx, locked.OrderID, map[string]any{"status": SolOrderStatusExpired}); err != nil {
			return err
		}
		locked.Status = SolOrderStatusExpired
		locked.UpdatedAt = now
		order = locked
		return nil
	})
	if err != nil {
		return store.BillingOrder{}, err
	}
	return order, nil
}

func (s *Service) recordFailedSolVerification(ctx context.Context, order store.BillingOrder, signature string, code string, cause error) error {
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
	return s.repo.UpdateBillingOrder(ctx, order.OrderID, map[string]any{"status": SolOrderStatusFailed})
}

func (s *Service) recordActivationKeyAudit(ctx context.Context, action string, actorID string, key store.ActivationAPIKey, order store.BillingOrder, extra map[string]string) {
	if s == nil || s.audit == nil || key.KeyID == "" {
		return
	}
	metadata := map[string]string{
		"key_id":         key.KeyID,
		"key_prefix":     key.KeyPrefix,
		"order_id":       firstNonEmpty(order.OrderID, key.BillingOrderID),
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

func (s *Service) recordCouponAudit(ctx context.Context, action string, actorID string, coupon store.BillingCoupon, extra map[string]any) {
	if s == nil || s.audit == nil || coupon.CouponID == "" {
		return
	}
	metadata := map[string]any{
		"coupon_id":     coupon.CouponID,
		"coupon_prefix": coupon.CodePrefix,
		"plan":          coupon.Plan,
		"status":        coupon.Status,
	}
	for name, value := range extra {
		if strings.TrimSpace(name) != "" {
			metadata[name] = value
		}
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		data = []byte("{}")
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    "",
		ActorID:    strings.TrimSpace(actorID),
		Action:     action,
		TargetType: "billing_coupon",
		TargetID:   coupon.CouponID,
		Metadata:   string(data),
		CreatedAt:  s.currentTime(),
	})
}

func (s *Service) couponView(coupon store.BillingCoupon, counts repository.CouponRedemptionCounts) CouponView {
	limits, _ := LimitsForPlan(coupon.Plan)
	return CouponView{
		CouponID:         coupon.CouponID,
		CodePrefix:       coupon.CodePrefix,
		Plan:             coupon.Plan,
		DisplayName:      firstNonEmpty(limits.DisplayName, coupon.Plan),
		DiscountLamports: coupon.DiscountLamports,
		MaxRedemptions:   coupon.MaxRedemptions,
		Status:           coupon.Status,
		OwnerNote:        coupon.OwnerNote,
		CreatedByUserID:  coupon.CreatedByUserID,
		ExpiresAt:        coupon.ExpiresAt,
		RevokedAt:        coupon.RevokedAt,
		Pending:          counts.Pending,
		Consumed:         counts.Consumed,
		Released:         counts.Released,
		CreatedAt:        coupon.CreatedAt.UTC(),
		UpdatedAt:        coupon.UpdatedAt.UTC(),
	}
}

func (s *Service) effectiveCouponStatus(coupon store.BillingCoupon, now time.Time) store.BillingCoupon {
	if coupon.Status == CouponStatusActive && coupon.ExpiresAt != nil && !now.Before(*coupon.ExpiresAt) {
		coupon.Status = CouponStatusExpired
	}
	return coupon
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

func (s *Service) solPaymentOrderView(order store.BillingOrder) SolPaymentOrderView {
	limits, _ := LimitsForPlan(order.Plan)
	return SolPaymentOrderView{
		OrderID:                      order.OrderID,
		GuildID:                      order.GuildID,
		BillingOwnerUserID:           order.BillingOwnerUserID,
		Plan:                         order.Plan,
		DisplayName:                  firstNonEmpty(limits.DisplayName, order.Plan),
		ListLamports:                 order.ListLamports,
		DiscountLamports:             order.DiscountLamports,
		DueLamports:                  order.DueLamports,
		ExpectedLamports:             order.DueLamports,
		AmountSOL:                    formatLamports(order.DueLamports),
		CouponPrefix:                 order.CouponPrefix,
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

func verifyTransactionForOrder(transaction SolanaTransaction, order store.BillingOrder) (store.SolPaymentTransaction, error) {
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
	if transfer.AmountLamports < order.DueLamports {
		return store.SolPaymentTransaction{}, fmt.Errorf("native SOL transfer underpaid order: got %d lamports, expected %d", transfer.AmountLamports, order.DueLamports)
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

func solanaPayURL(order store.BillingOrder) string {
	return ""
}

func buildSolPaymentTransactionBase64(order store.BillingOrder, payerWallet string, blockhash string) (string, error) {
	payer, err := decodeBase58Fixed(payerWallet, 32)
	if err != nil {
		return "", fmt.Errorf("invalid payer wallet: %w", err)
	}
	destination, err := decodeBase58Fixed(order.DestinationWallet, 32)
	if err != nil {
		return "", fmt.Errorf("invalid destination wallet: %w", err)
	}
	recentBlockhash, err := decodeBase58Fixed(blockhash, 32)
	if err != nil {
		return "", fmt.Errorf("invalid recent blockhash: %w", err)
	}
	systemProgram, _ := decodeBase58Fixed("11111111111111111111111111111111", 32)
	memoProgram, _ := decodeBase58Fixed("MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr", 32)

	var message bytes.Buffer
	message.Write([]byte{1, 0, 2})
	message.Write(compactLength(4))
	message.Write(payer)
	message.Write(destination)
	message.Write(systemProgram)
	message.Write(memoProgram)
	message.Write(recentBlockhash)
	message.Write(compactLength(2))

	transferData := make([]byte, 12)
	binary.LittleEndian.PutUint32(transferData[:4], 2)
	binary.LittleEndian.PutUint64(transferData[4:], uint64(order.DueLamports))
	writeCompiledInstruction(&message, 2, []byte{0, 1}, transferData)
	writeCompiledInstruction(&message, 3, nil, []byte(firstNonEmpty(order.Reference, order.OrderID)))

	var transaction bytes.Buffer
	transaction.Write(compactLength(1))
	transaction.Write(make([]byte, 64))
	transaction.Write(message.Bytes())
	return base64.StdEncoding.EncodeToString(transaction.Bytes()), nil
}

func writeCompiledInstruction(buffer *bytes.Buffer, programIndex byte, accounts []byte, data []byte) {
	buffer.WriteByte(programIndex)
	buffer.Write(compactLength(len(accounts)))
	buffer.Write(accounts)
	buffer.Write(compactLength(len(data)))
	buffer.Write(data)
}

func compactLength(length int) []byte {
	if length < 0 {
		length = 0
	}
	var out []byte
	value := uint(length)
	for {
		elem := byte(value & 0x7f)
		value >>= 7
		if value == 0 {
			out = append(out, elem)
			break
		}
		out = append(out, elem|0x80)
	}
	return out
}

func decodeBase58Fixed(value string, size int) ([]byte, error) {
	decoded, err := decodeBase58(value)
	if err != nil {
		return nil, err
	}
	if len(decoded) != size {
		return nil, fmt.Errorf("expected %d decoded bytes, got %d", size, len(decoded))
	}
	return decoded, nil
}

func decodeBase58(value string) ([]byte, error) {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("empty base58 value")
	}
	indices := make(map[rune]int64, len(alphabet))
	for i, char := range alphabet {
		indices[char] = int64(i)
	}
	decoded := big.NewInt(0)
	base := big.NewInt(58)
	for _, char := range value {
		index, ok := indices[char]
		if !ok {
			return nil, fmt.Errorf("invalid base58 character %q", char)
		}
		decoded.Mul(decoded, base)
		decoded.Add(decoded, big.NewInt(index))
	}
	leadingZeroes := 0
	for _, char := range value {
		if char != '1' {
			break
		}
		leadingZeroes++
	}
	result := append(make([]byte, leadingZeroes), decoded.Bytes()...)
	return result, nil
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

func couponCodeHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

func couponCodePrefix(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func newActivationAPIKey() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	secret := base64.RawURLEncoding.EncodeToString(bytes)
	return "panda_act_" + secret, nil
}

func newCouponCode() (string, error) {
	first, err := randomBase58(6)
	if err != nil {
		return "", err
	}
	second, err := randomBase58(6)
	if err != nil {
		return "", err
	}
	return "PANDA-" + strings.ToUpper(first) + "-" + strings.ToUpper(second), nil
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
