package discord

import (
	"context"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	botgateway "github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
)

type retryVoiceManager struct {
	mu           sync.Mutex
	daveSessions *daveSessionFactory
	conns        []*retryVoiceConn
}

func (m *retryVoiceManager) HandleVoiceStateUpdate(botgateway.EventVoiceStateUpdate) {}

func (m *retryVoiceManager) HandleVoiceServerUpdate(botgateway.EventVoiceServerUpdate) {}

func (m *retryVoiceManager) CreateConn(guildID snowflake.ID) voice.Conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	conn := &retryVoiceConn{
		guildID:      guildID,
		attempt:      len(m.conns) + 1,
		daveSessions: m.daveSessions,
	}
	m.conns = append(m.conns, conn)
	return conn
}

func (m *retryVoiceManager) GetConn(snowflake.ID) voice.Conn {
	return nil
}

func (m *retryVoiceManager) Conns() iter.Seq[voice.Conn] {
	return func(yield func(voice.Conn) bool) {
		m.mu.Lock()
		conns := append([]*retryVoiceConn(nil), m.conns...)
		m.mu.Unlock()
		for _, conn := range conns {
			if !yield(conn) {
				return
			}
		}
	}
}

func (m *retryVoiceManager) RemoveConn(guildID snowflake.ID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index, conn := range m.conns {
		if conn.GuildID() == guildID {
			m.conns = append(m.conns[:index], m.conns[index+1:]...)
			return
		}
	}
}

func (m *retryVoiceManager) Close(ctx context.Context) {
	for conn := range m.Conns() {
		conn.Close(ctx)
	}
}

func (m *retryVoiceManager) snapshot() []*retryVoiceConn {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*retryVoiceConn(nil), m.conns...)
}

type retryVoiceConn struct {
	mu           sync.Mutex
	guildID      snowflake.ID
	channelID    *snowflake.ID
	attempt      int
	closed       bool
	daveSessions *daveSessionFactory
}

func (c *retryVoiceConn) Gateway() voice.Gateway {
	return nil
}

func (c *retryVoiceConn) UDP() voice.UDPConn {
	return nil
}

func (c *retryVoiceConn) ChannelID() *snowflake.ID {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.channelID == nil {
		return nil
	}
	channelID := *c.channelID
	return &channelID
}

func (c *retryVoiceConn) GuildID() snowflake.ID {
	return c.guildID
}

func (c *retryVoiceConn) UserIDBySSRC(uint32) snowflake.ID {
	return 0
}

func (c *retryVoiceConn) SetSpeaking(context.Context, voice.SpeakingFlags) error {
	return nil
}

func (c *retryVoiceConn) SetOpusFrameProvider(voice.OpusFrameProvider) {}

func (c *retryVoiceConn) SetOpusFrameReceiver(voice.OpusFrameReceiver) {}

func (c *retryVoiceConn) SetEventHandlerFunc(voice.EventHandlerFunc) {}

func (c *retryVoiceConn) Open(_ context.Context, channelID snowflake.ID, _ bool, _ bool) error {
	c.mu.Lock()
	c.channelID = &channelID
	c.mu.Unlock()
	session := &trackedDaveSession{
		factory: c.daveSessions,
		ready:   c.attempt > 1,
		readyCh: make(chan struct{}),
	}
	c.daveSessions.bind(godave.ChannelID(channelID), session)
	return nil
}

func (c *retryVoiceConn) Close(context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

func (c *retryVoiceConn) HandleVoiceStateUpdate(botgateway.EventVoiceStateUpdate) {}

func (c *retryVoiceConn) HandleVoiceServerUpdate(botgateway.EventVoiceServerUpdate) {}

func (c *retryVoiceConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func TestMusicVoiceConnectorRetriesDaveReadinessTimeout(t *testing.T) {
	previousDaveReadyTimeout := daveReadyTimeout
	previousReconnectDelay := voiceReconnectDelay
	daveReadyTimeout = 5 * time.Millisecond
	voiceReconnectDelay = time.Millisecond
	t.Cleanup(func() {
		daveReadyTimeout = previousDaveReadyTimeout
		voiceReconnectDelay = previousReconnectDelay
	})

	daveSessions := newDaveSessionFactory(nil)
	voiceManager := &retryVoiceManager{daveSessions: daveSessions}
	connector := newMusicVoiceConnector(&bot.Client{VoiceManager: voiceManager}, nil, daveSessions)

	session, err := connector.Connect(context.Background(), "1489749058179432480", "1518069557192036564")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if session.ChannelID() != "1518069557192036564" {
		t.Fatalf("unexpected connected channel: %s", session.ChannelID())
	}
	conns := voiceManager.snapshot()
	if len(conns) != 2 {
		t.Fatalf("expected one retry after DAVE readiness timeout, got %d connection(s)", len(conns))
	}
	if !conns[0].isClosed() {
		t.Fatal("expected first unready connection to be closed")
	}
	if conns[1].isClosed() {
		t.Fatal("expected second ready connection to stay open")
	}
}
