package contextsvc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeProvider struct {
	messages []Message
	err      error
}

func (f fakeProvider) FetchMessage(_ context.Context, ref MessageRef) (Message, error) {
	if f.err != nil {
		return Message{}, f.err
	}
	for _, message := range f.messages {
		if message.MessageID == ref.MessageID {
			return message, nil
		}
	}
	return Message{}, errors.New("not found")
}

func (f fakeProvider) FetchRecentMessages(_ context.Context, ref ChannelRef, limit int) ([]Message, error) {
	if f.err != nil {
		return nil, f.err
	}
	var result []Message
	for _, message := range f.messages {
		if message.GuildID == ref.GuildID && message.ChannelID == ref.ChannelID {
			result = append(result, message)
		}
	}
	if len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result, nil
}

func TestMessageContextPacksCitationAndRedactsSecrets(t *testing.T) {
	service := NewService(fakeProvider{messages: []Message{{
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		MessageID: "message-1",
		AuthorID:  "user-1",
		Content:   "Deploy key is sk-123456789012.",
		CreatedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC),
	}}})

	packed, err := service.MessageContext(context.Background(), MessageRef{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "message-1"})
	if err != nil {
		t.Fatalf("MessageContext: %v", err)
	}
	if !strings.Contains(packed.Text, "[S1]") || !strings.Contains(packed.Text, "message_id=message-1") {
		t.Fatalf("citation missing from packed context: %s", packed.Text)
	}
	if strings.Contains(packed.Text, "sk-123456789012") {
		t.Fatalf("secret was not redacted: %s", packed.Text)
	}
	if len(packed.Citations) != 1 || packed.Citations[0].Label != "S1" {
		t.Fatalf("unexpected citations: %+v", packed.Citations)
	}
	if packed.Citations[0].URL != "https://discord.com/channels/guild-1/channel-1/message-1" {
		t.Fatalf("unexpected citation URL: %+v", packed.Citations[0])
	}
}

func TestRecentMessagesContextClampsLimit(t *testing.T) {
	service := NewService(fakeProvider{messages: []Message{
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "message-1", Content: "first"},
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "message-2", Content: "second"},
	}})

	packed, err := service.RecentMessagesContext(context.Background(), ChannelRef{GuildID: "guild-1", ChannelID: "channel-1"}, 1)
	if err != nil {
		t.Fatalf("RecentMessagesContext: %v", err)
	}
	if strings.Contains(packed.Text, "message-1") || !strings.Contains(packed.Text, "message-2") {
		t.Fatalf("expected only the most recent message, got %s", packed.Text)
	}
}

func TestRecentUserMessagesContextFiltersAuthor(t *testing.T) {
	service := NewService(fakeProvider{messages: []Message{
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "message-1", AuthorID: "user-1", Content: "keep this"},
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "message-2", AuthorID: "user-2", Content: "drop this"},
		{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "message-3", AuthorID: "user-1", Content: "keep this too"},
	}})

	packed, err := service.RecentUserMessagesContext(context.Background(), ChannelRef{GuildID: "guild-1", ChannelID: "channel-1"}, "user-1", 10)
	if err != nil {
		t.Fatalf("RecentUserMessagesContext: %v", err)
	}
	if !strings.Contains(packed.Text, "keep this") || !strings.Contains(packed.Text, "keep this too") || strings.Contains(packed.Text, "drop this") {
		t.Fatalf("unexpected packed user history: %s", packed.Text)
	}
	if len(packed.Citations) != 2 {
		t.Fatalf("expected two citations, got %+v", packed.Citations)
	}
}
