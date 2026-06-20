package discord

import (
	"context"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	contextsvc "github.com/sn0w/panda2/internal/context"
)

type messageFetcher interface {
	GetMessage(channelID snowflake.ID, messageID snowflake.ID, opts ...rest.RequestOpt) (*disgoDiscord.Message, error)
	GetMessages(channelID snowflake.ID, around snowflake.ID, before snowflake.ID, after snowflake.ID, limit int, opts ...rest.RequestOpt) ([]disgoDiscord.Message, error)
}

type ContextProvider struct {
	messages messageFetcher
}

func NewContextProvider(messages messageFetcher) *ContextProvider {
	return &ContextProvider{messages: messages}
}

func (p *ContextProvider) FetchMessage(_ context.Context, ref contextsvc.MessageRef) (contextsvc.Message, error) {
	channelID, err := snowflake.Parse(ref.ChannelID)
	if err != nil {
		return contextsvc.Message{}, err
	}
	messageID, err := snowflake.Parse(ref.MessageID)
	if err != nil {
		return contextsvc.Message{}, err
	}
	message, err := p.messages.GetMessage(channelID, messageID)
	if err != nil {
		return contextsvc.Message{}, err
	}
	return toContextMessage(*message, ref.GuildID), nil
}

func (p *ContextProvider) FetchRecentMessages(_ context.Context, ref contextsvc.ChannelRef, limit int) ([]contextsvc.Message, error) {
	channelID, err := snowflake.Parse(ref.ChannelID)
	if err != nil {
		return nil, err
	}
	messages, err := p.messages.GetMessages(channelID, 0, 0, 0, limit)
	if err != nil {
		return nil, err
	}
	result := make([]contextsvc.Message, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		result = append(result, toContextMessage(messages[i], ref.GuildID))
	}
	return result, nil
}

func toContextMessage(message disgoDiscord.Message, fallbackGuildID string) contextsvc.Message {
	guildID := fallbackGuildID
	if message.GuildID != nil {
		guildID = message.GuildID.String()
	}
	return contextsvc.Message{
		GuildID:   guildID,
		ChannelID: message.ChannelID.String(),
		MessageID: message.ID.String(),
		AuthorID:  message.Author.ID.String(),
		Content:   message.Content,
		CreatedAt: message.CreatedAt,
	}
}
