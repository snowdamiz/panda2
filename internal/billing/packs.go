package billing

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	PackTrial    = "trial"
	PackStarter  = "starter"
	PackPlus     = "plus"
	PackPro      = "pro"
	PackBusiness = "business"

	StatusTrialing  = "trialing"
	StatusActive    = "active"
	StatusPastDue   = "past_due"
	StatusGrace     = "grace"
	StatusReadOnly  = "read_only"
	StatusSuspended = "suspended"
	StatusCanceled  = "canceled"
	StatusDepleted  = "depleted"

	GraceActive    = "active"
	GraceTrialing  = "trialing"
	GracePastDue   = "past_due"
	GraceGrace     = "grace"
	GraceReadOnly  = "read_only"
	GraceSuspended = "suspended"
	GraceCanceled  = "canceled"
	GraceDepleted  = "depleted"

	ActionAssistantModelRound = "assistant_model_round"
	ActionRoutingCheck        = "routing_check"
	ActionWebSearch           = "web_search"
	ActionImageInspection     = "image_inspection"
	ActionImageGeneration     = "image_generation"
	ActionYouTubeSearch       = "youtube_search"
	ActionYouTubeSummary      = "youtube_summary"
	ActionYouTubeClip         = "youtube_clip"
	ActionKnowledgeWrite      = "knowledge_write"
	ActionStorageRent         = "storage_rent"
	ActionScheduledRun        = "scheduled_run"
	ActionMusicPlayback       = "music_playback"

	// Compatibility metric names used by older call sites. NormalizeMetric maps
	// them onto credit actions; no metric-specific limit buckets are enforced.
	MetricAIResponse           = "ai_response"
	MetricWebSearch            = "web_search"
	MetricImageGeneration      = "image_generation"
	MetricKnowledgeStorageByte = "knowledge_storage_byte"
	MetricScheduledRun         = "scheduled_run"
	MetricMusicMinute          = "music_minute"

	ProviderTrial  = "trial"
	ProviderSol    = "sol"
	ProviderCoupon = "coupon"
	ProviderManual = "manual"

	CreditReservationPending   = "pending"
	CreditReservationCommitted = "committed"
	CreditReservationReleased  = "released"
	CreditReservationExpired   = "expired"

	CreditLedgerGrant       = "grant"
	CreditLedgerReserve     = "reserve"
	CreditLedgerCommit      = "commit"
	CreditLedgerRelease     = "release"
	CreditLedgerAdjustment  = "adjustment"
	CreditLedgerRefund      = "refund"
	CreditLedgerExpiry      = "expiry"
	CreditLedgerStorageRent = "storage_rent"
)

const (
	TrialDuration = 14 * 24 * time.Hour
	GraceDuration = 3 * 24 * time.Hour
)

type PackDefinition struct {
	Pack                  string
	DisplayName           string
	PriceCents            int
	Credits               int64
	RetentionDays         int
	KnowledgeStorageBytes int64
	ExpiresAfter          time.Duration
}

type ActionQuoteRequest struct {
	Action           string
	RequestID        string
	InputTokens      int
	OutputTokens     int
	AudioSeconds     int
	Bytes            int64
	Resolution       string
	RenderedClips    int
	MaxRenderedClips int
	Minutes          int
	Metadata         map[string]any
}

type CreditQuote struct {
	Action          string
	RequestID       string
	ExpectedCredits int64
	MaxCredits      int64
	Metadata        map[string]any
}

var packDefinitions = map[string]PackDefinition{
	PackTrial: {
		Pack:                  PackTrial,
		DisplayName:           "Trial Pack",
		PriceCents:            0,
		Credits:               1500,
		RetentionDays:         14,
		KnowledgeStorageBytes: 25 * 1024 * 1024,
		ExpiresAfter:          TrialDuration,
	},
	PackStarter: {
		Pack:                  PackStarter,
		DisplayName:           "Starter Pack",
		PriceCents:            1900,
		Credits:               10000,
		RetentionDays:         30,
		KnowledgeStorageBytes: 100 * 1024 * 1024,
		ExpiresAfter:          30 * 24 * time.Hour,
	},
	PackPlus: {
		Pack:                  PackPlus,
		DisplayName:           "Plus Pack",
		PriceCents:            4900,
		Credits:               30000,
		RetentionDays:         90,
		KnowledgeStorageBytes: 500 * 1024 * 1024,
		ExpiresAfter:          90 * 24 * time.Hour,
	},
	PackPro: {
		Pack:                  PackPro,
		DisplayName:           "Pro Pack",
		PriceCents:            9900,
		Credits:               75000,
		RetentionDays:         180,
		KnowledgeStorageBytes: 2 * 1024 * 1024 * 1024,
		ExpiresAfter:          180 * 24 * time.Hour,
	},
	PackBusiness: {
		Pack:                  PackBusiness,
		DisplayName:           "Business Pack",
		PriceCents:            24900,
		Credits:               220000,
		RetentionDays:         365,
		KnowledgeStorageBytes: 10 * 1024 * 1024 * 1024,
		ExpiresAfter:          365 * 24 * time.Hour,
	},
}

func NormalizePack(pack string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(pack))
	switch normalized {
	case PackTrial, PackStarter, PackPlus, PackPro, PackBusiness:
		return normalized, true
	default:
		return "", false
	}
}

func PackForID(pack string) (PackDefinition, bool) {
	normalized, ok := NormalizePack(pack)
	if !ok {
		return PackDefinition{}, false
	}
	return packDefinitions[normalized], true
}

func PackCatalog() []PackDefinition {
	return []PackDefinition{
		packDefinitions[PackTrial],
		packDefinitions[PackStarter],
		packDefinitions[PackPlus],
		packDefinitions[PackPro],
		packDefinitions[PackBusiness],
	}
}

func NormalizeAction(action string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(action))
	switch normalized {
	case ActionAssistantModelRound, ActionRoutingCheck, ActionWebSearch, ActionImageInspection,
		ActionImageGeneration, ActionYouTubeSearch, ActionYouTubeSummary, ActionYouTubeClip,
		ActionKnowledgeWrite, ActionStorageRent, ActionScheduledRun, ActionMusicPlayback:
		return normalized, true
	default:
		return "", false
	}
}

func NormalizeMetric(metric string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(metric))
	switch normalized {
	case MetricAIResponse:
		return ActionAssistantModelRound, true
	case MetricWebSearch:
		return ActionWebSearch, true
	case MetricImageGeneration:
		return ActionImageGeneration, true
	case MetricKnowledgeStorageByte:
		return ActionKnowledgeWrite, true
	case MetricScheduledRun:
		return ActionScheduledRun, true
	case MetricMusicMinute:
		return ActionMusicPlayback, true
	default:
		return NormalizeAction(normalized)
	}
}

func ActionLabel(action string) string {
	switch action {
	case ActionAssistantModelRound:
		return "assistant model round"
	case ActionRoutingCheck:
		return "routing check"
	case ActionWebSearch:
		return "web search"
	case ActionImageInspection:
		return "image inspection"
	case ActionImageGeneration:
		return "image generation"
	case ActionYouTubeSearch:
		return "YouTube search"
	case ActionYouTubeSummary:
		return "YouTube summary"
	case ActionYouTubeClip:
		return "YouTube clip generation"
	case ActionKnowledgeWrite:
		return "knowledge write"
	case ActionStorageRent:
		return "knowledge storage rent"
	case ActionScheduledRun:
		return "scheduled run"
	case ActionMusicPlayback:
		return "music playback"
	default:
		return strings.ReplaceAll(strings.TrimSpace(action), "_", " ")
	}
}

func MetricLabel(metric string) string {
	if action, ok := NormalizeMetric(metric); ok {
		return ActionLabel(action)
	}
	return strings.ReplaceAll(metric, "_", " ")
}

func IncludedLimit(_ PackDefinition, _ string) int64 {
	return math.MaxInt64
}

func FormatCredits(available, reserved int64) string {
	if reserved > 0 {
		return fmt.Sprintf("%d available, %d reserved", available, reserved)
	}
	return fmt.Sprintf("%d available", available)
}

func FormatUsage(used, limit int64, metric string) string {
	if limit == math.MaxInt64 {
		return fmt.Sprintf("%d credits", used)
	}
	if metric == MetricKnowledgeStorageByte {
		return fmt.Sprintf("%s / %s", formatBytes(used), formatBytes(limit))
	}
	return fmt.Sprintf("%d / %d", used, limit)
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}

func quoteForAction(request ActionQuoteRequest) (CreditQuote, error) {
	action, ok := NormalizeAction(request.Action)
	if !ok {
		return CreditQuote{}, fmt.Errorf("unsupported credit action")
	}
	metadata := cloneMetadata(request.Metadata)
	expected := int64(0)
	maxCredits := int64(0)
	switch action {
	case ActionAssistantModelRound:
		expected = 4 + tokenSurcharge(request.InputTokens, 4000, 1000) + tokenSurcharge(request.OutputTokens, 900, 500)
		maxCredits = expected
	case ActionRoutingCheck:
		expected, maxCredits = 1, 1
	case ActionWebSearch:
		expected, maxCredits = 8, 8
	case ActionImageInspection:
		expected, maxCredits = 25, 25
	case ActionImageGeneration:
		expected = imageGenerationCredits(request.Resolution)
		maxCredits = expected
	case ActionYouTubeSearch:
		expected, maxCredits = 3, 3
	case ActionYouTubeSummary:
		minutes := billableMinutes(request.AudioSeconds, request.Minutes)
		if minutes == 0 {
			expected, maxCredits = 20, 260
		} else {
			expected = 20 + int64(minutes*4)
			maxCredits = expected
		}
	case ActionYouTubeClip:
		minutes := billableMinutes(request.AudioSeconds, request.Minutes)
		clips := request.RenderedClips
		if clips <= 0 {
			clips = request.MaxRenderedClips
		}
		if clips <= 0 {
			clips = 3
		}
		if minutes == 0 {
			expected, maxCredits = 250, 1150
		} else {
			expected = 250 + int64(minutes*5) + int64(clips*200)
			maxCredits = expected
		}
	case ActionKnowledgeWrite:
		expected = knowledgeWriteCredits(request.Bytes)
		maxCredits = expected
	case ActionStorageRent:
		expected = storageRentCredits(request.Bytes)
		maxCredits = expected
	case ActionScheduledRun:
		expected, maxCredits = 2, 2
	case ActionMusicPlayback:
		minutes := billableMinutes(0, request.Minutes)
		if minutes == 0 {
			minutes = 5
		}
		increments := ceilDiv(minutes, 5)
		expected = int64(increments)
		maxCredits = expected
	}
	if expected <= 0 {
		expected = 1
	}
	if maxCredits < expected {
		maxCredits = expected
	}
	metadata["expected_credits"] = expected
	metadata["max_credits"] = maxCredits
	return CreditQuote{
		Action:          action,
		RequestID:       strings.TrimSpace(request.RequestID),
		ExpectedCredits: expected,
		MaxCredits:      maxCredits,
		Metadata:        metadata,
	}, nil
}

func tokenSurcharge(tokens, included, step int) int64 {
	if tokens <= included || step <= 0 {
		return 0
	}
	return int64(ceilDiv(tokens-included, step))
}

func imageGenerationCredits(resolution string) int64 {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "512", "512x512", "512px":
		return 75
	case "2k", "2048", "2048x2048":
		return 500
	case "4k", "4096", "4096x4096":
		return 1200
	default:
		return 150
	}
}

func billableMinutes(seconds, minutes int) int {
	if minutes > 0 {
		return minutes
	}
	if seconds <= 0 {
		return 0
	}
	return ceilDiv(seconds, 60)
}

func knowledgeWriteCredits(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	return int64(ceilDiv64(bytes, 10*1024) * 2)
}

func storageRentCredits(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	mb := ceilDiv64(bytes, 1024*1024)
	return int64(ceilDiv64(mb, 30))
}

func ceilDiv(value, by int) int {
	if value <= 0 {
		return 0
	}
	return (value + by - 1) / by
}

func ceilDiv64(value, by int64) int64 {
	if value <= 0 {
		return 0
	}
	return (value + by - 1) / by
}

func cloneMetadata(metadata map[string]any) map[string]any {
	clone := make(map[string]any, len(metadata)+2)
	for key, value := range metadata {
		if strings.TrimSpace(key) != "" {
			clone[key] = value
		}
	}
	return clone
}
