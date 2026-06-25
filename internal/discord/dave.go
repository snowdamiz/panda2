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
	logger = daveSessionLogger(logger)
	return &daveSessionFactory{
		logger:    logger,
		byChannel: map[godave.ChannelID]*trackedDaveSession{},
	}
}

func (f *daveSessionFactory) New(logger *slog.Logger, userID godave.UserID, callbacks godave.Callbacks) godave.Session {
	if logger == nil {
		logger = f.logger
	}
	logger = daveSessionLogger(logger)
	return &trackedDaveSession{
		Session: davesession.New(logger, userID, callbacks),
		factory: f,
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
	s.Session.OnSelectProtocolAck(protocolVersion)
}

func (s *trackedDaveSession) OnDavePrepareTransition(transitionID uint16, protocolVersion uint16) {
	if transitionID == 0 && protocolVersion > 0 {
		s.mu.Lock()
		s.executeZeroTransition = true
		s.mu.Unlock()
	}
	s.Session.OnDavePrepareTransition(transitionID, protocolVersion)
}

func (s *trackedDaveSession) OnDavePrepareEpoch(epoch int, protocolVersion uint16) {
	s.resetReady()
	s.Session.OnDavePrepareEpoch(epoch, protocolVersion)
}

func (s *trackedDaveSession) OnDaveExecuteTransition(transitionID uint16) {
	s.Session.OnDaveExecuteTransition(transitionID)
	s.markReadyIfInnerReady()
}

func (s *trackedDaveSession) OnDaveMLSExternalSenderPackage(externalSenderPackage []byte) {
	s.Session.OnDaveMLSExternalSenderPackage(externalSenderPackage)
}

func (s *trackedDaveSession) OnDaveMLSProposals(proposals []byte) {
	s.Session.OnDaveMLSProposals(proposals)
	s.executeZeroTransitionIfNeeded()
}

func (s *trackedDaveSession) OnDaveMLSPrepareCommitTransition(transitionID uint16, commitMessage []byte) {
	s.Session.OnDaveMLSPrepareCommitTransition(transitionID, commitMessage)
	s.markReadyIfInnerReady()
}

func (s *trackedDaveSession) OnDaveMLSWelcome(transitionID uint16, welcomeMessage []byte) {
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
}

func daveSessionLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	return slog.New(minLevelHandler{handler: logger.Handler(), min: slog.LevelWarn})
}

type minLevelHandler struct {
	handler slog.Handler
	min     slog.Level
}

func (h minLevelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.min && h.handler.Enabled(ctx, level)
}

func (h minLevelHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Level < h.min {
		return nil
	}
	return h.handler.Handle(ctx, record)
}

func (h minLevelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return minLevelHandler{handler: h.handler.WithAttrs(attrs), min: h.min}
}

func (h minLevelHandler) WithGroup(name string) slog.Handler {
	return minLevelHandler{handler: h.handler.WithGroup(name), min: h.min}
}
