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
	voiceConnectAttempts = 2
)

var (
	voiceConnectTimeout = 30 * time.Second
	daveReadyTimeout    = 45 * time.Second
	voiceReconnectDelay = 750 * time.Millisecond
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
			if err := c.waitForDave(ctx, channelID); err == nil {
				return &discordVoiceSession{conn: existing}, nil
			} else if ctx.Err() != nil {
				return nil, err
			} else {
				c.logger.Warn("existing voice connection is not media-ready; reconnecting",
					slog.Any("err", err),
					slog.String("guild_id", guildID.String()),
					slog.String("voice_channel_id", channelID.String()),
				)
			}
		}
		closeVoiceConn(existing)
	}

	var lastErr error
	for attempt := 1; attempt <= voiceConnectAttempts; attempt++ {
		conn := c.client.VoiceManager.CreateConn(guildID)
		c.logger.Info("joining voice channel",
			slog.String("guild_id", guildID.String()),
			slog.String("voice_channel_id", channelID.String()),
			slog.Int("attempt", attempt),
		)
		connectCtx, cancel := context.WithTimeout(ctx, voiceConnectTimeout)
		err := conn.Open(connectCtx, channelID, false, true)
		cancel()
		if err != nil {
			closeVoiceConn(conn)
			lastErr = fmt.Errorf("%w: open voice connection: %v", music.ErrVoiceConnection, err)
			if ctx.Err() != nil || attempt == voiceConnectAttempts {
				return nil, lastErr
			}
			c.waitBeforeReconnect(ctx, guildID, channelID, attempt, lastErr)
			continue
		}
		c.logger.Info("voice connection opened",
			slog.String("guild_id", guildID.String()),
			slog.String("voice_channel_id", channelID.String()),
			slog.Int("attempt", attempt),
		)
		if err := c.waitForDave(ctx, channelID); err != nil {
			closeVoiceConn(conn)
			lastErr = err
			if ctx.Err() != nil || attempt == voiceConnectAttempts {
				return nil, lastErr
			}
			c.waitBeforeReconnect(ctx, guildID, channelID, attempt, lastErr)
			continue
		}
		return &discordVoiceSession{conn: conn}, nil
	}
	return nil, lastErr
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

func (c *musicVoiceConnector) waitBeforeReconnect(ctx context.Context, guildID snowflake.ID, channelID snowflake.ID, attempt int, err error) {
	c.logger.Warn("voice connection attempt failed; retrying",
		slog.Any("err", err),
		slog.String("guild_id", guildID.String()),
		slog.String("voice_channel_id", channelID.String()),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", voiceConnectAttempts),
	)
	if voiceReconnectDelay <= 0 {
		return
	}
	timer := time.NewTimer(voiceReconnectDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func closeVoiceConn(conn voice.Conn) {
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn.Close(ctx)
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
