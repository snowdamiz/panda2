package alerts

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sn0w/panda2/internal/composed"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

const (
	PackSecurity   = "security"
	PackModeration = "moderation"
	PackCommunity  = "community"
	PackAll        = "all"
)

type AuditRecorder interface {
	Record(ctx context.Context, event store.AuditEvent) error
}

type DeliverySender interface {
	SendAlert(ctx context.Context, delivery Delivery) error
}

type Service struct {
	rules    *repository.AlertRuleRepository
	delivery DeliverySender
	audit    AuditRecorder
	now      func() time.Time
}

type Delivery struct {
	GuildID      string
	ChannelID    string
	Pack         string
	Risk         string
	EventType    string
	Summary      string
	ActorID      string
	TargetID     string
	Suggested    string
	PendingCount int
}

type Preview struct {
	Pack        string
	Description string
	EventTypes  []string
	Risk        string
}

func NewService(rules *repository.AlertRuleRepository) *Service {
	return &Service{rules: rules, now: time.Now}
}

func (s *Service) WithDeliverySender(sender DeliverySender) *Service {
	s.delivery = sender
	return s
}

func (s *Service) WithAuditRecorder(recorder AuditRecorder) *Service {
	s.audit = recorder
	return s
}

func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) Enable(ctx context.Context, guildID, actorID, pack, channelID string, cooldown time.Duration) (store.AlertRule, error) {
	pack = normalizePack(pack)
	if _, ok := packDefinitions()[pack]; !ok {
		return store.AlertRule{}, fmt.Errorf("unknown alert pack %q", pack)
	}
	if cooldown <= 0 {
		cooldown = 5 * time.Minute
	}
	rule, err := s.rules.Enable(ctx, store.AlertRule{
		GuildID:         guildID,
		Pack:            pack,
		ChannelID:       channelID,
		CooldownSeconds: int(cooldown.Seconds()),
		CreatedBy:       actorID,
	})
	if err == nil {
		s.recordAudit(ctx, guildID, actorID, "alert_pack.enable", pack, channelID)
	}
	return rule, err
}

func (s *Service) Disable(ctx context.Context, guildID, actorID, pack string) error {
	pack = normalizePack(pack)
	if err := s.rules.Disable(ctx, guildID, pack); err != nil {
		return err
	}
	s.recordAudit(ctx, guildID, actorID, "alert_pack.disable", pack, "")
	return nil
}

func (s *Service) List(ctx context.Context, guildID string) ([]store.AlertRule, error) {
	return s.rules.List(ctx, guildID)
}

func (s *Service) Previews() []Preview {
	defs := packDefinitions()
	order := []string{PackSecurity, PackModeration, PackCommunity, PackAll}
	previews := make([]Preview, 0, len(order))
	for _, pack := range order {
		def := defs[pack]
		previews = append(previews, Preview{
			Pack:        pack,
			Description: def.description,
			EventTypes:  append([]string{}, def.eventTypes...),
			Risk:        def.risk,
		})
	}
	return previews
}

func (s *Service) Test(ctx context.Context, guildID, pack string) error {
	pack = normalizePack(pack)
	rules, err := s.rules.Enabled(ctx, guildID)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if rule.Pack != pack {
			continue
		}
		return s.send(ctx, rule, Delivery{
			GuildID:   guildID,
			ChannelID: rule.ChannelID,
			Pack:      pack,
			Risk:      packDefinitions()[pack].risk,
			EventType: "alert_pack.test",
			Summary:   "Test alert from Panda.",
			Suggested: "If this reached the right channel, the alert pack is ready.",
		})
	}
	return repository.ErrNotFound
}

func (s *Service) HandleDiscordEvent(ctx context.Context, event store.DiscordEvent) {
	if s == nil || s.rules == nil || strings.TrimSpace(event.GuildID) == "" {
		return
	}
	rules, err := s.rules.Enabled(ctx, event.GuildID)
	if err != nil {
		return
	}
	for _, rule := range rules {
		def, ok := packDefinitions()[rule.Pack]
		if !ok || !def.matches(event.EventType) {
			continue
		}
		now := s.now().UTC()
		if rule.LastSentAt != nil && rule.CooldownSeconds > 0 && rule.LastSentAt.Add(time.Duration(rule.CooldownSeconds)*time.Second).After(now) {
			_ = s.rules.IncrementPending(ctx, rule.ID, now)
			continue
		}
		delivery := Delivery{
			GuildID:      event.GuildID,
			ChannelID:    rule.ChannelID,
			Pack:         rule.Pack,
			Risk:         riskForEvent(rule.Pack, event.EventType),
			EventType:    event.EventType,
			Summary:      eventSummary(event),
			ActorID:      event.UserID,
			TargetID:     firstNonEmpty(event.MessageID, event.ChannelID),
			Suggested:    suggestedAction(event.EventType),
			PendingCount: rule.PendingCount,
		}
		if err := s.send(ctx, rule, delivery); err == nil {
			_ = s.rules.MarkSent(ctx, rule.ID, now)
		}
	}
}

func (s *Service) send(ctx context.Context, rule store.AlertRule, delivery Delivery) error {
	if s.delivery == nil {
		return fmt.Errorf("alert delivery is not configured")
	}
	if delivery.ChannelID == "" {
		delivery.ChannelID = rule.ChannelID
	}
	return s.delivery.SendAlert(ctx, delivery)
}

func (s *Service) recordAudit(ctx context.Context, guildID, actorID, action, pack, channelID string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, store.AuditEvent{
		GuildID:    guildID,
		ActorID:    actorID,
		Action:     action,
		TargetType: "alert_pack",
		TargetID:   pack,
		Metadata:   fmt.Sprintf(`{"channel_id":%q}`, channelID),
	})
}

type packDefinition struct {
	description string
	eventTypes  []string
	risk        string
}

func (d packDefinition) matches(eventType string) bool {
	for _, candidate := range d.eventTypes {
		if candidate == eventType {
			return true
		}
	}
	return false
}

func packDefinitions() map[string]packDefinition {
	securityEvents := []string{
		composed.EventWebhooksUpdated,
		composed.EventInviteCreated,
		composed.EventInviteDeleted,
		composed.EventRoleCreated,
		composed.EventRoleUpdated,
		composed.EventRoleDeleted,
		composed.EventChannelCreated,
		composed.EventChannelUpdated,
		composed.EventChannelDeleted,
		composed.EventThreadUpdated,
	}
	moderationEvents := []string{
		composed.EventAutoModerationAction,
		composed.EventAutoModerationCreated,
		composed.EventAutoModerationUpdated,
		composed.EventAutoModerationDeleted,
		composed.EventGuildBan,
		composed.EventGuildUnban,
	}
	communityEvents := []string{
		composed.EventScheduledCreated,
		composed.EventScheduledUpdated,
		composed.EventScheduledDeleted,
		composed.EventScheduledUserAdded,
		composed.EventScheduledUserRemoved,
	}
	all := append(append([]string{}, securityEvents...), moderationEvents...)
	all = append(all, communityEvents...)
	return map[string]packDefinition{
		PackSecurity: {
			description: "Webhooks, invites, roles, channels, and thread setting changes.",
			eventTypes:  securityEvents,
			risk:        "high",
		},
		PackModeration: {
			description: "Auto-moderation actions/rules and ban/unban events.",
			eventTypes:  moderationEvents,
			risk:        "medium",
		},
		PackCommunity: {
			description: "Scheduled server event changes and participation changes.",
			eventTypes:  communityEvents,
			risk:        "low",
		},
		PackAll: {
			description: "All recommended moderation and server-log alerts.",
			eventTypes:  all,
			risk:        "mixed",
		},
	}
}

func riskForEvent(pack, eventType string) string {
	switch eventType {
	case composed.EventWebhooksUpdated, composed.EventRoleCreated, composed.EventRoleUpdated, composed.EventRoleDeleted:
		return "high"
	case composed.EventInviteCreated, composed.EventInviteDeleted, composed.EventChannelDeleted, composed.EventGuildBan:
		return "medium"
	default:
		if def, ok := packDefinitions()[pack]; ok {
			return def.risk
		}
		return "low"
	}
}

func eventSummary(event store.DiscordEvent) string {
	summary := strings.TrimSpace(event.Summary)
	if summary == "" {
		summary = "Discord event recorded"
	}
	if event.EventType != "" {
		summary += " (`" + event.EventType + "`)"
	}
	return summary
}

func suggestedAction(eventType string) string {
	switch eventType {
	case composed.EventWebhooksUpdated:
		return "Review recent audit logs and verify the webhook destination."
	case composed.EventInviteCreated, composed.EventInviteDeleted:
		return "Check whether the invite change was expected."
	case composed.EventRoleCreated, composed.EventRoleUpdated, composed.EventRoleDeleted:
		return "Verify role permissions and hierarchy before they are used broadly."
	case composed.EventAutoModerationAction:
		return "Review the auto-moderation action and affected user context."
	case composed.EventGuildBan, composed.EventGuildUnban:
		return "Confirm the moderation action matches server policy."
	case composed.EventChannelUpdated, composed.EventThreadUpdated:
		return "Check changed visibility, permissions, and archive settings."
	default:
		return "Review the event and take action if it was unexpected."
	}
}

func normalizePack(pack string) string {
	pack = strings.ToLower(strings.TrimSpace(pack))
	switch pack {
	case "", "recommended":
		return PackSecurity
	default:
		return pack
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func FormatPending(count int) string {
	if count <= 0 {
		return ""
	}
	return " Batched " + strconv.Itoa(count) + " similar event(s) during cooldown."
}
