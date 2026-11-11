package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const publicRefreshWait = 6 * time.Second

type publisherEvent struct {
	kind string
	data string
}

type pendingRefresh struct {
	done      chan struct{}
	refreshed bool
}

type publisherStream struct {
	events        chan publisherEvent
	done          chan struct{}
	pending       *pendingRefresh
	refreshQueued bool
	closeOnce     sync.Once
}

func newPublisherStream() *publisherStream {
	return &publisherStream{
		events: make(chan publisherEvent, 2),
		done:   make(chan struct{}),
	}
}

func (s *publisherStream) close() {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.pending != nil {
			close(s.pending.done)
			s.pending = nil
		}
	})
}

func (a *App) publisherEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		a.methodNotAllowed(w)
		return
	}
	publisher, ok := a.requirePublisher(w, r)
	if !ok {
		return
	}
	stream, registered, err := a.registerPublisherStream(r.Context(), publisher)
	if err != nil {
		a.internalError(w)
		return
	}
	if !registered {
		a.writeError(w, http.StatusUnauthorized, "发布者凭证已失效", "unauthorized")
		return
	}
	defer a.detachPublisherStream(publisher.connectionID, stream)

	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	if err := controller.Flush(); err != nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-stream.done:
			return
		case event := <-stream.events:
			if event.kind == "refresh" {
				a.acknowledgeRefresh(publisher.connectionID, stream)
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.kind, event.data); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}

func (a *App) registerPublisherStream(ctx context.Context, publisher publisherIdentity) (*publisherStream, bool, error) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	var found int
	err := a.store.db.QueryRowContext(ctx, `SELECT 1 FROM publishers WHERE connection_id=? AND token_hash=?`, publisher.connectionID, publisher.tokenHash).Scan(&found)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, false, err
	}
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	stream := newPublisherStream()
	if old := a.streams[publisher.connectionID]; old != nil {
		old.close()
	}
	a.streams[publisher.connectionID] = stream
	return stream, true, nil
}

func (a *App) detachPublisherStream(connectionID string, stream *publisherStream) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	if a.streams[connectionID] == stream {
		delete(a.streams, connectionID)
		stream.close()
	}
}

func (a *App) closePublisherStreamLocked(connectionID string) {
	if stream := a.streams[connectionID]; stream != nil {
		delete(a.streams, connectionID)
		stream.close()
	}
}

func (a *App) closeStreams() {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	for connectionID := range a.streams {
		a.closePublisherStreamLocked(connectionID)
	}
}

func (a *App) notifyVisibilityClosedLocked(connectionID string) {
	stream := a.streams[connectionID]
	if stream == nil {
		return
	}
	if stream.pending != nil {
		close(stream.pending.done)
		stream.pending = nil
	}
	select {
	case <-stream.done:
		return
	case stream.events <- publisherEvent{kind: "visibility", data: `{"enabled":false}`}:
	default:
	}
}

func (a *App) requestRefreshLocked(connectionID string) *pendingRefresh {
	stream := a.streams[connectionID]
	if stream == nil {
		return nil
	}
	select {
	case <-stream.done:
		return nil
	default:
	}
	if stream.pending != nil {
		return stream.pending
	}
	pending := &pendingRefresh{done: make(chan struct{})}
	if !stream.refreshQueued {
		select {
		case stream.events <- publisherEvent{kind: "refresh", data: `{}`}:
			stream.refreshQueued = true
		default:
			return nil
		}
	}
	stream.pending = pending
	return pending
}

func (a *App) acknowledgeRefresh(connectionID string, stream *publisherStream) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	if a.streams[connectionID] == stream {
		stream.refreshQueued = false
	}
}

func (a *App) completePendingRefresh(connectionID string) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	stream := a.streams[connectionID]
	if stream == nil || stream.pending == nil {
		return
	}
	pending := stream.pending
	stream.pending = nil
	pending.refreshed = true
	close(pending.done)
}

func (a *App) pendingRefreshReceived(pending *pendingRefresh) bool {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	return pending.refreshed
}

func (a *App) expirePendingRefresh(connectionID string, stream *publisherStream, pending *pendingRefresh) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	if a.streams[connectionID] == stream && stream.pending == pending {
		stream.pending = nil
		close(pending.done)
	}
}
