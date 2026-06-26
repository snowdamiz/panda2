package music

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestBufferedOpusProviderWaitsForPrebuffer(t *testing.T) {
	source := newChannelOpusProvider()
	provider := newBufferedOpusProvider(source, 4, 2)
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := provider.WaitReady(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected first wait to time out, got %v", err)
	}

	source.send([]byte{0x01})
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = provider.WaitReady(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected wait with one frame to time out, got %v", err)
	}

	source.send([]byte{0x02})
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = provider.WaitReady(ctx); err != nil {
		t.Fatalf("wait ready: %v", err)
	}
	assertNextFrame(t, provider, []byte{0x01})
	assertNextFrame(t, provider, []byte{0x02})
}

func TestBufferedOpusProviderUnderflowTimeout(t *testing.T) {
	source := newChannelOpusProvider()
	provider := newBufferedOpusProvider(source, 4, 1)
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, underflow, err := provider.ProvideOpusFrameWithin(ctx, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("underflow read error: %v", err)
	}
	if !underflow {
		t.Fatalf("expected underflow")
	}
	if len(frame) != 0 {
		t.Fatalf("expected no frame on underflow, got %#v", frame)
	}

	source.send([]byte{0x03})
	frame, underflow, err = provider.ProvideOpusFrameWithin(ctx, time.Second)
	if err != nil {
		t.Fatalf("frame read error: %v", err)
	}
	if underflow {
		t.Fatalf("did not expect underflow")
	}
	if !bytes.Equal(frame, []byte{0x03}) {
		t.Fatalf("unexpected frame %#v", frame)
	}
}

func TestBufferedOpusProviderDrainsFramesBeforeEndError(t *testing.T) {
	boom := errors.New("boom")
	source := newChannelOpusProvider()
	provider := newBufferedOpusProvider(source, 4, 1)
	defer provider.Close()

	source.send([]byte{0x04})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := provider.WaitReady(ctx); err != nil {
		t.Fatalf("wait ready: %v", err)
	}
	source.sendErr(boom)

	assertNextFrame(t, provider, []byte{0x04})
	if _, err := provider.ProvideOpusFrame(); !errors.Is(err, boom) {
		t.Fatalf("expected stored error, got %v", err)
	}
}

func TestBufferedOpusProviderTreatsEOFBeforeFramesAsNotReady(t *testing.T) {
	source := newChannelOpusProvider()
	provider := newBufferedOpusProvider(source, 4, 1)
	defer provider.Close()

	source.sendErr(io.EOF)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := provider.WaitReady(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF before frames to fail readiness, got %v", err)
	}
	if _, err := provider.ProvideOpusFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func assertNextFrame(t *testing.T, provider *bufferedOpusProvider, expected []byte) {
	t.Helper()
	frame, err := provider.ProvideOpusFrame()
	if err != nil {
		t.Fatalf("next frame error: %v", err)
	}
	if !bytes.Equal(frame, expected) {
		t.Fatalf("unexpected frame %#v, expected %#v", frame, expected)
	}
}

type channelOpusProvider struct {
	results chan channelOpusResult
	close   chan struct{}
	once    sync.Once
}

type channelOpusResult struct {
	frame []byte
	err   error
}

func newChannelOpusProvider() *channelOpusProvider {
	return &channelOpusProvider{
		results: make(chan channelOpusResult),
		close:   make(chan struct{}),
	}
}

func (p *channelOpusProvider) ProvideOpusFrame() ([]byte, error) {
	select {
	case result := <-p.results:
		if result.err != nil {
			return nil, result.err
		}
		return append([]byte(nil), result.frame...), nil
	case <-p.close:
		return nil, io.EOF
	}
}

func (p *channelOpusProvider) Close() {
	p.once.Do(func() {
		close(p.close)
	})
}

func (p *channelOpusProvider) send(frame []byte) {
	p.results <- channelOpusResult{frame: frame}
}

func (p *channelOpusProvider) sendErr(err error) {
	p.results <- channelOpusResult{err: err}
}
