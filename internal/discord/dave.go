package discord

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	davesession "github.com/thomas-vilte/dave-go/session"
)

type daveSessionFactory struct {
	logger *slog.Logger

	mu        sync.Mutex
	byChannel map[godave.ChannelID]*trackedDaveSession
}

func newDaveSessionFactory(logger *slog.Logger) *daveSessionFactory {
	if logger == nil {
		logger = slog.Default()
	}
	return &daveSessionFactory{
		logger:    logger,
		byChannel: map[godave.ChannelID]*trackedDaveSession{},
	}
}

func (f *daveSessionFactory) New(logger *slog.Logger, userID godave.UserID, callbacks godave.Callbacks) godave.Session {
	if logger == nil {
		logger = f.logger
	}
	return &trackedDaveSession{
		Session: davesession.New(logger, userID, callbacks),
		factory: f,
		logger:  logger,
		readyCh: make(chan struct{}),
		userID:  userID,
	}
}

func (f *daveSessionFactory) waitReady(ctx context.Context, channelID snowflake.ID) error {
	daveChannelID := godave.ChannelID(channelID)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		session := f.session(daveChannelID)
		if session != nil {
			return session.waitReady(ctx)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for dave session bind: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (f *daveSessionFactory) bind(channelID godave.ChannelID, session *trackedDaveSession) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byChannel[channelID] = session
}

func (f *daveSessionFactory) unbind(channelID godave.ChannelID, session *trackedDaveSession) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byChannel[channelID] == session {
		delete(f.byChannel, channelID)
	}
}

func (f *daveSessionFactory) session(channelID godave.ChannelID) *trackedDaveSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byChannel[channelID]
}

type trackedDaveSession struct {
	godave.Session

	factory *daveSessionFactory
	logger  *slog.Logger
	userID  godave.UserID

	mu                    sync.Mutex
	channelID             godave.ChannelID
	ready                 bool
	readyCh               chan struct{}
	executeZeroTransition bool
}

func (s *trackedDaveSession) SetChannelID(channelID godave.ChannelID) {
	s.mu.Lock()
	previous := s.channelID
	if previous != channelID {
		s.channelID = channelID
		s.ready = false
		s.readyCh = make(chan struct{})
		s.executeZeroTransition = false
	}
	s.mu.Unlock()

	if previous != 0 && previous != channelID {
		s.factory.unbind(previous, s)
	}
	s.factory.bind(channelID, s)
	s.Session.SetChannelID(channelID)
}

func (s *trackedDaveSession) OnSelectProtocolAck(protocolVersion uint16) {
	s.mu.Lock()
	s.executeZeroTransition = false
	s.mu.Unlock()
	s.resetReady()
	s.logDaveEvent("dave select protocol ack", slog.Int("protocol_version", int(protocolVersion)))
	s.Session.OnSelectProtocolAck(protocolVersion)
}

func (s *trackedDaveSession) OnDavePrepareTransition(transitionID uint16, protocolVersion uint16) {
	if transitionID == 0 && protocolVersion > 0 {
		s.mu.Lock()
		s.executeZeroTransition = true
		s.mu.Unlock()
	}
	s.logDaveEvent("dave prepare transition",
		slog.Int("transition_id", int(transitionID)),
		slog.Int("protocol_version", int(protocolVersion)),
	)
	s.Session.OnDavePrepareTransition(transitionID, protocolVersion)
}

func (s *trackedDaveSession) OnDavePrepareEpoch(epoch int, protocolVersion uint16) {
	s.resetReady()
	s.logDaveEvent("dave prepare epoch", slog.Int("epoch", epoch), slog.Int("protocol_version", int(protocolVersion)))
	s.Session.OnDavePrepareEpoch(epoch, protocolVersion)
}

func (s *trackedDaveSession) OnDaveExecuteTransition(transitionID uint16) {
	s.logDaveEvent("dave execute transition", slog.Int("transition_id", int(transitionID)))
	s.Session.OnDaveExecuteTransition(transitionID)
	s.markReadyIfInnerReady()
}

func (s *trackedDaveSession) OnDaveMLSExternalSenderPackage(externalSenderPackage []byte) {
	s.logDaveEvent("dave mls external sender package", slog.Int("size", len(externalSenderPackage)))
	s.Session.OnDaveMLSExternalSenderPackage(externalSenderPackage)
}

func (s *trackedDaveSession) OnDaveMLSProposals(proposals []byte) {
	s.logDaveEvent("dave mls proposals", slog.Int("size", len(proposals)))
	s.Session.OnDaveMLSProposals(proposals)
	s.executeZeroTransitionIfNeeded()
}

func (s *trackedDaveSession) OnDaveMLSPrepareCommitTransition(transitionID uint16, commitMessage []byte) {
	s.logDaveEvent("dave mls prepare commit transition",
		slog.Int("transition_id", int(transitionID)),
		slog.Int("size", len(commitMessage)),
	)
	s.Session.OnDaveMLSPrepareCommitTransition(transitionID, commitMessage)
	s.markReadyIfInnerReady()
}

func (s *trackedDaveSession) OnDaveMLSWelcome(transitionID uint16, welcomeMessage []byte) {
	s.logDaveEvent("dave mls welcome", slog.Int("transition_id", int(transitionID)), slog.Int("size", len(welcomeMessage)))
	s.Session.OnDaveMLSWelcome(transitionID, welcomeMessage)
	s.markReadyIfInnerReady()
}

func (s *trackedDaveSession) executeZeroTransitionIfNeeded() {
	s.mu.Lock()
	shouldExecute := s.executeZeroTransition && !s.ready
	s.mu.Unlock()
	if !shouldExecute {
		return
	}
	s.logDaveEvent("dave executing pending zero transition")
	s.Session.OnDaveExecuteTransition(0)
	s.markReadyIfInnerReady()
}

func (s *trackedDaveSession) waitReady(ctx context.Context) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		if s.ready {
			s.mu.Unlock()
			return nil
		}
		readyCh := s.readyCh
		s.mu.Unlock()

		select {
		case <-readyCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *trackedDaveSession) resetReady() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready {
		s.ready = false
		s.readyCh = make(chan struct{})
	}
}

func (s *trackedDaveSession) markReadyIfInnerReady() {
	readyReporter, ok := s.Session.(interface{ Ready() bool })
	if !ok || !readyReporter.Ready() {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready {
		return
	}
	s.ready = true
	s.executeZeroTransition = false
	close(s.readyCh)
	if s.logger != nil {
		s.logger.Info("dave media ready", slog.String("user_id", string(s.userID)), slog.Uint64("channel_id", uint64(s.channelID)))
	}
}

func (s *trackedDaveSession) logDaveEvent(message string, attrs ...slog.Attr) {
	if s.logger == nil {
		return
	}
	values := make([]any, 0, len(attrs)+2)
	values = append(values,
		slog.String("user_id", string(s.userID)),
		slog.Uint64("channel_id", uint64(s.channelID)),
	)
	for _, attr := range attrs {
		values = append(values, attr)
	}
	s.logger.Info(message, values...)
}
