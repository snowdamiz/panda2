package discord

import (
	"context"
	"strings"

	disgoDiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/commands"
	"github.com/sn0w/panda2/internal/textutil"
)

type threadCreator interface {
	CreateThread(channelID snowflake.ID, threadCreate disgoDiscord.ThreadCreate, opts ...rest.RequestOpt) (thread *disgoDiscord.GuildThread, err error)
}

type ThreadManager struct {
	threads threadCreator
}

func NewThreadManager(threads threadCreator) *ThreadManager {
	return &ThreadManager{threads: threads}
}

func (m *ThreadManager) EnsureChatThread(_ context.Context, request commands.ThreadRequest) (commands.Thread, error) {
	channelID, err := snowflake.Parse(request.ChannelID)
	if err != nil {
		return commands.Thread{}, err
	}
	thread, err := m.threads.CreateThread(channelID, disgoDiscord.GuildPublicThreadCreate{
		Name:                safeThreadName(request.Title),
		AutoArchiveDuration: disgoDiscord.AutoArchiveDuration24h,
	})
	if err != nil {
		return commands.Thread{}, err
	}
	return commands.Thread{
		ID:      thread.ID().String(),
		Name:    thread.Name(),
		Created: true,
	}, nil
}

func safeThreadName(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if value == "" {
		return "Panda chat"
	}
	if len(value) > 90 {
		value = textutil.Truncate(value, 90, "")
	}
	return value
}
