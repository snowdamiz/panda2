package music

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

type bufferedOpusProvider struct {
	source OpusFrameProvider
	frames chan []byte
	done   chan struct{}
	ready  chan struct{}

	closeOnce sync.Once
	readyOnce sync.Once
	errMu     sync.Mutex
	err       error
}

func newBufferedOpusProvider(source OpusFrameProvider, capacity int, prebuffer int) *bufferedOpusProvider {
	if capacity < 1 {
		capacity = 1
	}
	if prebuffer < 1 {
		prebuffer = 1
	}
	if prebuffer > capacity {
		prebuffer = capacity
	}
	provider := &bufferedOpusProvider{
		source: source,
		frames: make(chan []byte, capacity),
		done:   make(chan struct{}),
		ready:  make(chan struct{}),
	}
	go provider.fill(prebuffer)
	return provider
}

func (p *bufferedOpusProvider) WaitReady(ctx context.Context) error {
	select {
	case <-p.ready:
		err := p.storedErr()
		if err != nil && !errors.Is(err, io.EOF) && len(p.frames) == 0 {
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *bufferedOpusProvider) BufferedFrames() int {
	return len(p.frames)
}

func (p *bufferedOpusProvider) ProvideOpusFrame() ([]byte, error) {
	frame, ok := <-p.frames
	if ok {
		return frame, nil
	}
	return nil, p.endErr()
}

func (p *bufferedOpusProvider) ProvideOpusFrameWithin(ctx context.Context, timeout time.Duration) ([]byte, bool, error) {
	if timeout <= 0 {
		select {
		case frame, ok := <-p.frames:
			if ok {
				return frame, false, nil
			}
			return nil, false, p.endErr()
		case <-ctx.Done():
			return nil, false, ctx.Err()
		default:
			return nil, true, nil
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case frame, ok := <-p.frames:
		if ok {
			return frame, false, nil
		}
		return nil, false, p.endErr()
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-timer.C:
		return nil, true, nil
	}
}

func (p *bufferedOpusProvider) Close() {
	p.closeOnce.Do(func() {
		close(p.done)
		p.markReady()
		if p.source != nil {
			p.source.Close()
		}
	})
}

func (p *bufferedOpusProvider) fill(prebuffer int) {
	defer close(p.frames)
	defer p.markReady()

	buffered := 0
	for {
		frame, err := p.source.ProvideOpusFrame()
		if err != nil {
			p.setErr(err)
			return
		}
		if len(frame) == 0 {
			continue
		}

		select {
		case p.frames <- frame:
			buffered++
			if buffered >= prebuffer {
				p.markReady()
			}
		case <-p.done:
			return
		}
	}
}

func (p *bufferedOpusProvider) markReady() {
	p.readyOnce.Do(func() {
		close(p.ready)
	})
}

func (p *bufferedOpusProvider) setErr(err error) {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	p.err = err
}

func (p *bufferedOpusProvider) storedErr() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.err
}

func (p *bufferedOpusProvider) endErr() error {
	err := p.storedErr()
	if err == nil || errors.Is(err, io.EOF) {
		return io.EOF
	}
	return err
}
