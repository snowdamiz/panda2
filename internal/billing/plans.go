package billing

import (
	"fmt"
	"strings"
	"time"
)

const (
	PlanTrial    = "trial"
	PlanStarter  = "starter"
	PlanPlus     = "plus"
	PlanPro      = "pro"
	PlanBusiness = "business"

	StatusTrialing  = "trialing"
	StatusActive    = "active"
	StatusPastDue   = "past_due"
	StatusGrace     = "grace"
	StatusReadOnly  = "read_only"
	StatusSuspended = "suspended"
	StatusCanceled  = "canceled"

	GraceActive    = "active"
	GraceTrialing  = "trialing"
	GracePastDue   = "past_due"
	GraceGrace     = "grace"
	GraceReadOnly  = "read_only"
	GraceSuspended = "suspended"
	GraceCanceled  = "canceled"

	MetricAIResponse           = "ai_response"
	MetricWebSearch            = "web_search"
	MetricKnowledgeStorageByte = "knowledge_storage_byte"
	MetricScheduledRun         = "scheduled_run"
	MetricMusicMinute          = "music_minute"

	ProviderTrial   = "trial"
	ProviderDiscord = "discord"
	ProviderStripe  = "stripe"
	ProviderManual  = "manual"

	UsageReservationPending  = "pending"
	UsageReservationConsumed = "consumed"
	UsageReservationReleased = "released"
)

const (
	TrialDuration = 14 * 24 * time.Hour
	GraceDuration = 3 * 24 * time.Hour
)

type PlanLimits struct {
	Plan                  string
	DisplayName           string
	PriceCents            int
	AIResponses           int
	WebSearches           int
	KnowledgeStorageBytes int64
	Schedules             int
	RetentionDays         int
	MusicEnabled          bool
	PremiumToolsEnabled   bool
}

var planLimits = map[string]PlanLimits{
	PlanTrial: {
		Plan:                  PlanTrial,
		DisplayName:           "Trial",
		PriceCents:            0,
		AIResponses:           250,
		WebSearches:           20,
		KnowledgeStorageBytes: 25 * 1024 * 1024,
		Schedules:             3,
		RetentionDays:         14,
		MusicEnabled:          false,
		PremiumToolsEnabled:   false,
	},
	PlanStarter: {
		Plan:                  PlanStarter,
		DisplayName:           "Starter",
		PriceCents:            1900,
		AIResponses:           2000,
		WebSearches:           100,
		KnowledgeStorageBytes: 100 * 1024 * 1024,
		Schedules:             10,
		RetentionDays:         30,
		MusicEnabled:          true,
		PremiumToolsEnabled:   true,
	},
	PlanPlus: {
		Plan:                  PlanPlus,
		DisplayName:           "Plus",
		PriceCents:            4900,
		AIResponses:           5000,
		WebSearches:           400,
		KnowledgeStorageBytes: 500 * 1024 * 1024,
		Schedules:             50,
		RetentionDays:         90,
		MusicEnabled:          true,
		PremiumToolsEnabled:   true,
	},
	PlanPro: {
		Plan:                  PlanPro,
		DisplayName:           "Pro",
		PriceCents:            9900,
		AIResponses:           10000,
		WebSearches:           1000,
		KnowledgeStorageBytes: 2 * 1024 * 1024 * 1024,
		Schedules:             200,
		RetentionDays:         180,
		MusicEnabled:          true,
		PremiumToolsEnabled:   true,
	},
	PlanBusiness: {
		Plan:                  PlanBusiness,
		DisplayName:           "Business",
		PriceCents:            24900,
		AIResponses:           25000,
		WebSearches:           2000,
		KnowledgeStorageBytes: 10 * 1024 * 1024 * 1024,
		Schedules:             1000,
		RetentionDays:         365,
		MusicEnabled:          true,
		PremiumToolsEnabled:   true,
	},
}

func NormalizePlan(plan string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(plan))
	switch normalized {
	case PlanTrial, PlanStarter, PlanPlus, PlanPro, PlanBusiness:
		return normalized, true
	default:
		return "", false
	}
}

func LimitsForPlan(plan string) (PlanLimits, bool) {
	normalized, ok := NormalizePlan(plan)
	if !ok {
		return PlanLimits{}, false
	}
	return planLimits[normalized], true
}

func PlanCatalog() []PlanLimits {
	return []PlanLimits{
		planLimits[PlanTrial],
		planLimits[PlanStarter],
		planLimits[PlanPlus],
		planLimits[PlanPro],
		planLimits[PlanBusiness],
	}
}

func NormalizeMetric(metric string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(metric))
	switch normalized {
	case MetricAIResponse, MetricWebSearch, MetricKnowledgeStorageByte, MetricScheduledRun, MetricMusicMinute:
		return normalized, true
	default:
		return "", false
	}
}

func MetricLabel(metric string) string {
	switch metric {
	case MetricAIResponse:
		return "AI responses"
	case MetricWebSearch:
		return "web searches"
	case MetricKnowledgeStorageByte:
		return "knowledge storage"
	case MetricScheduledRun:
		return "scheduled runs"
	case MetricMusicMinute:
		return "music minutes"
	default:
		return strings.ReplaceAll(metric, "_", " ")
	}
}

func IncludedLimit(limits PlanLimits, metric string) int64 {
	switch metric {
	case MetricAIResponse:
		return int64(limits.AIResponses)
	case MetricWebSearch:
		return int64(limits.WebSearches)
	case MetricKnowledgeStorageByte:
		return limits.KnowledgeStorageBytes
	case MetricScheduledRun:
		return 1<<63 - 1
	case MetricMusicMinute:
		if limits.MusicEnabled {
			return 1<<63 - 1
		}
		return 0
	default:
		return 0
	}
}

func FormatUsage(used, limit int64, metric string) string {
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
