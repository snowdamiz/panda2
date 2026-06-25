package discord

import (
	"strings"
	"sync"

	"github.com/disgoorg/snowflake/v2"
	"github.com/sn0w/panda2/internal/commands"
)

type naturalMessageQueue struct {
	mu      sync.Mutex
	queues  map[string][]func()
	running map[string]struct{}
}

func newNaturalMessageQueue() *naturalMessageQueue {
	return &naturalMessageQueue{
		queues:  map[string][]func(){},
		running: map[string]struct{}{},
	}
}

func (q *naturalMessageQueue) enqueue(key string, task func()) {
	if q == nil || task == nil {
		return
	}
	key = normalizeNaturalMessageKey(key)

	q.mu.Lock()
	q.queues[key] = append(q.queues[key], task)
	if _, ok := q.running[key]; ok {
		q.mu.Unlock()
		return
	}
	q.running[key] = struct{}{}
	q.mu.Unlock()

	go q.drain(key)
}

func (q *naturalMessageQueue) drain(key string) {
	for {
		task, ok := q.next(key)
		if !ok {
			return
		}
		task()
	}
}

func (q *naturalMessageQueue) next(key string) (func(), bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	tasks := q.queues[key]
	if len(tasks) == 0 {
		delete(q.queues, key)
		delete(q.running, key)
		return nil, false
	}

	task := tasks[0]
	tasks[0] = nil
	q.queues[key] = tasks[1:]
	return task, true
}

func normalizeNaturalMessageKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "global"
	}
	return key
}

func naturalMessageKey(channelID snowflake.ID, request commands.Request) string {
	channel := strings.TrimSpace(request.ChannelID)
	if channel == "" && channelID != 0 {
		channel = channelID.String()
	}
	if channel == "" {
		channel = "unknown"
	}
	if guild := strings.TrimSpace(request.GuildID); guild != "" {
		return guild + ":" + channel
	}
	return "dm:" + channel
}
