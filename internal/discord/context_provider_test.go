package discord

import (
	"testing"
	"time"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	contextsvc "github.com/sn0w/panda2/internal/context"
)

type fakeMessageFetcher struct {
	message  *disgoDiscord.Message
	messages []disgoDiscord.Message
}

func (f fakeMessageFetcher) GetMessage(channelID snowflake.ID, messageID snowflake.ID, opts ...rest.RequestOpt) (*disgoDiscord.Message, error) {
	return f.message, nil
}

func (f fakeMessageFetcher) GetMessages(channelID snowflake.ID, around snowflake.ID, before snowflake.ID, after snowflake.ID, limit int, opts ...rest.RequestOpt) ([]disgoDiscord.Message, error) {
	if len(f.messages) > limit {
		return f.messages[:limit], nil
	}
	return f.messages, nil
}

func TestContextProviderFetchMessage(t *testing.T) {
	guildID := snowflake.MustParse("100000000000000001")
	channelID := snowflake.MustParse("100000000000000002")
	messageID := snowflake.MustParse("100000000000000003")
	authorID := snowflake.MustParse("100000000000000004")
	created := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	provider := NewContextProvider(fakeMessageFetcher{message: &disgoDiscord.Message{
		ID:        messageID,
		GuildID:   &guildID,
		ChannelID: channelID,
		Author:    disgoDiscord.User{ID: authorID},
		Content:   "message content",
		CreatedAt: created,
	}})

	message, err := provider.FetchMessage(nil, contextsvc.MessageRef{
		GuildID:   guildID.String(),
		ChannelID: channelID.String(),
		MessageID: messageID.String(),
	})
	if err != nil {
		t.Fatalf("FetchMessage: %v", err)
	}
	if message.MessageID != messageID.String() || message.AuthorID != authorID.String() || message.CreatedAt != created {
		t.Fatalf("unexpected context message: %+v", message)
	}
}

func TestContextProviderFetchRecentMessagesReturnsChronologicalOrder(t *testing.T) {
	channelID := snowflake.MustParse("100000000000000002")
	newestID := snowflake.MustParse("100000000000000010")
	oldestID := snowflake.MustParse("100000000000000011")
	provider := NewContextProvider(fakeMessageFetcher{messages: []disgoDiscord.Message{
		{ID: newestID, ChannelID: channelID, Content: "newest"},
		{ID: oldestID, ChannelID: channelID, Content: "oldest"},
	}})

	messages, err := provider.FetchRecentMessages(nil, contextsvc.ChannelRef{
		GuildID:   "100000000000000001",
		ChannelID: channelID.String(),
	}, 2)
	if err != nil {
		t.Fatalf("FetchRecentMessages: %v", err)
	}
	if len(messages) != 2 || messages[0].MessageID != oldestID.String() || messages[1].MessageID != newestID.String() {
		t.Fatalf("expected chronological messages, got %+v", messages)
	}
}
