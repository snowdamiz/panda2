package discord

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/music"
)

const (
	voiceConnectTimeout = 30 * time.Second
	daveReadyTimeout    = 30 * time.Second
)

type musicVoiceConnector struct {
	client       *bot.Client
	logger       *slog.Logger
	daveSessions *daveSessionFactory
}

func newMusicVoiceConnector(client *bot.Client, logger *slog.Logger, daveSessions *daveSessionFactory) music.VoiceConnector {
	if logger == nil {
		logger = slog.Default()
	}
	return &musicVoiceConnector{client: client, logger: logger, daveSessions: daveSessions}
}

func (c *musicVoiceConnector) Connect(ctx context.Context, guildIDValue string, channelIDValue string) (music.VoiceSession, error) {
	if c.client == nil || c.client.VoiceManager == nil {
		return nil, fmt.Errorf("%w: discord voice manager is not configured", music.ErrVoiceConnection)
	}
	guildID, err := snowflake.Parse(guildIDValue)
	if err != nil {
		return nil, fmt.Errorf("%w: parse guild id: %v", music.ErrVoiceConnection, err)
	}
	channelID, err := snowflake.Parse(channelIDValue)
	if err != nil {
		return nil, fmt.Errorf("%w: parse voice channel id: %v", music.ErrVoiceConnection, err)
	}

	if existing := c.client.VoiceManager.GetConn(guildID); existing != nil {
		if existingChannelID := existing.ChannelID(); existingChannelID != nil && *existingChannelID == channelID {
			if err := c.waitForDave(ctx, channelID); err != nil {
				return nil, err
			}
			return &discordVoiceSession{conn: existing}, nil
		}
		existing.Close(ctx)
	}

	conn := c.client.VoiceManager.CreateConn(guildID)
	c.logger.Info("joining voice channel", slog.String("guild_id", guildID.String()), slog.String("voice_channel_id", channelID.String()))
	connectCtx, cancel := context.WithTimeout(ctx, voiceConnectTimeout)
	defer cancel()
	if err := conn.Open(connectCtx, channelID, false, true); err != nil {
		conn.Close(ctx)
		return nil, fmt.Errorf("%w: open voice connection: %v", music.ErrVoiceConnection, err)
	}
	c.logger.Info("voice connection opened", slog.String("guild_id", guildID.String()), slog.String("voice_channel_id", channelID.String()))
	if err := c.waitForDave(ctx, channelID); err != nil {
		conn.Close(ctx)
		return nil, err
	}
	return &discordVoiceSession{conn: conn}, nil
}

func (c *musicVoiceConnector) waitForDave(ctx context.Context, channelID snowflake.ID) error {
	if c.daveSessions == nil {
		return nil
	}
	readyCtx, cancel := context.WithTimeout(ctx, daveReadyTimeout)
	defer cancel()
	if err := c.daveSessions.waitReady(readyCtx, channelID); err != nil {
		return fmt.Errorf("%w: dave media session not ready: %v", music.ErrVoiceConnection, err)
	}
	return nil
}

type discordVoiceSession struct {
	conn voice.Conn
}

func (s *discordVoiceSession) ChannelID() string {
	if s == nil || s.conn == nil || s.conn.ChannelID() == nil {
		return ""
	}
	return s.conn.ChannelID().String()
}

func (s *discordVoiceSession) SetSpeaking(ctx context.Context, speaking bool) error {
	flag := voice.SpeakingFlagNone
	if speaking {
		flag = voice.SpeakingFlagMicrophone
	}
	return s.conn.SetSpeaking(ctx, flag)
}

func (s *discordVoiceSession) WriteOpus(ctx context.Context, frame []byte) error {
	deadline := time.Now().Add(5 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := s.conn.UDP().SetWriteDeadline(deadline); err != nil {
		return err
	}
	_, err := s.conn.UDP().Write(frame)
	return err
}

func (s *discordVoiceSession) Close(ctx context.Context) {
	if s == nil || s.conn == nil {
		return
	}
	s.conn.Close(ctx)
}
