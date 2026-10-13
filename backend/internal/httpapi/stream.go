package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/R0cker64/kubeportal/backend/internal/auth"
	"github.com/R0cker64/kubeportal/backend/internal/authz"
	"github.com/R0cker64/kubeportal/backend/internal/logs"
)

const (
	defaultLogWindow = time.Hour
	defaultLogLines  = 500
	// recheckInterval is how often a WebSocket re-validates its session and
	// re-resolves the user's teams.
	recheckInterval = 30 * time.Second
	wsWriteTimeout  = 10 * time.Second
	// watchDebounce collapses bursts of informer events.
	watchDebounce = time.Second
	// watchRefresh re-sends the list even without events, because health
	// reasons depend on time (e.g. a pod Pending for too long).
	watchRefresh = 30 * time.Second
)

// logFilter validates the text and level parameters.
func (a *api) logFilter(r *http.Request) (logs.Filter, bool) {
	q := r.URL.Query()
	f := logs.Filter{Text: q.Get("q"), Level: q.Get("level")}
	if !authz.ValidSearchText(f.Text, a.cfg.LogMaxSearchLen) {
		return f, false
	}
	return f, f.Level == "" || slices.Contains(logs.Levels, f.Level)
}

func (a *api) serviceLogs(w http.ResponseWriter, r *http.Request) {
	p, wl, ok := a.lookupService(w, r)
	if !ok {
		return
	}
	f, ok := a.logFilter(r)
	if !ok {
		writeError(w, r, http.StatusBadRequest)
		return
	}
	tr, limit, ok := a.logWindow(r)
	if !ok {
		writeError(w, r, http.StatusBadRequest)
		return
	}
	sel, err := p.LogSelector(target(wl))
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity)
		return
	}
	lines, err := a.deps.Logs.Query(r.Context(), sel, f, tr, limit)
	if err != nil {
		a.upstreamError(w, r, "loki query", err)
		return
	}
	if lines == nil {
		lines = []logs.Line{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"start": tr.Start, "end": tr.End, "limit": limit, "lines": lines,
	})
}

// logWindow parses start, end (RFC 3339) and limit, applying the configured
// maximum range and line count.
func (a *api) logWindow(r *http.Request) (authz.TimeRange, int, bool) {
	q := r.URL.Query()
	now := time.Now()
	end := now
	var start time.Time
	var err error
	if v := q.Get("end"); v != "" {
		if end, err = time.Parse(time.RFC3339, v); err != nil {
			return authz.TimeRange{}, 0, false
		}
	}
	if v := q.Get("start"); v != "" {
		if start, err = time.Parse(time.RFC3339, v); err != nil {
			return authz.TimeRange{}, 0, false
		}
	} else {
		start = end.Add(-defaultLogWindow)
	}
	tr, err := authz.NewTimeRange(start, end, now, a.cfg.LogMaxRange)
	if err != nil {
		return authz.TimeRange{}, 0, false
	}
	limit := min(defaultLogLines, a.cfg.LogMaxLines)
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > a.cfg.LogMaxLines {
			return authz.TimeRange{}, 0, false
		}
		limit = n
	}
	return tr, limit, true
}

// acceptWebSocket upgrades after checking the Origin against the public URL
// (CLAUDE.md rule 7). The session was already validated by requireSession.
func (a *api) acceptWebSocket(w http.ResponseWriter, r *http.Request) (*websocket.Conn, bool) {
	if r.Header.Get("Origin") != publicOrigin(a.cfg.PublicURL) {
		writeError(w, r, http.StatusForbidden)
		return nil, false
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{a.cfg.PublicURL.Host},
	})
	if err != nil {
		a.log.WarnContext(r.Context(), "websocket accept", "request_id", RequestID(r.Context()), "error", err)
		return nil, false
	}
	// Clients never send data; anything larger than a control frame is abuse.
	conn.SetReadLimit(512)
	return conn, true
}

func writeWS(ctx context.Context, conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, data)
}

// streamContext ends when the client goes away (CloseRead) or the session
// stops being valid; the latest principal is delivered on the channel.
func (a *api) streamContext(r *http.Request, conn *websocket.Conn) (context.Context, <-chan authz.Principal, context.CancelFunc) {
	ctx := conn.CloseRead(r.Context())
	ctx, cancel := context.WithCancel(ctx)
	principals := make(chan authz.Principal, 1)
	go func() {
		t := time.NewTicker(recheckInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p, err := a.authenticate(r.WithContext(ctx))
				if err != nil {
					if !errors.Is(err, auth.ErrNoSession) {
						a.log.WarnContext(ctx, "websocket recheck", "error", err)
					}
					_ = conn.Close(websocket.StatusPolicyViolation, "session ended")
					cancel()
					return
				}
				select {
				case principals <- p:
				default:
				}
			}
		}
	}()
	return ctx, principals, cancel
}

// serviceLogsLive streams new log lines of one service over a WebSocket.
func (a *api) serviceLogsLive(w http.ResponseWriter, r *http.Request) {
	p, wl, ok := a.lookupService(w, r)
	if !ok {
		return
	}
	f, ok := a.logFilter(r)
	if !ok {
		writeError(w, r, http.StatusBadRequest)
		return
	}
	sel, err := p.LogSelector(target(wl))
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity)
		return
	}
	release, ok := a.tails.acquire(p.Subject)
	if !ok {
		writeError(w, r, http.StatusTooManyRequests)
		return
	}
	defer release()

	conn, ok := a.acceptWebSocket(w, r)
	if !ok {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	ctx, principals, cancel := a.streamContext(r, conn)
	defer cancel()

	// Stop the tail if the user loses access to this workload's team.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case np := <-principals:
				if !np.CanSeeWorkload(wl.Labels) {
					_ = conn.Close(websocket.StatusPolicyViolation, "access revoked")
					cancel()
					return
				}
			}
		}
	}()

	err = a.deps.Logs.Tail(ctx, sel, f, func(lines []logs.Line) error {
		return writeWS(ctx, conn, map[string]any{"lines": lines})
	})
	if err != nil && ctx.Err() == nil {
		a.log.WarnContext(r.Context(), "loki tail", "request_id", RequestID(r.Context()), "error", err)
		_ = conn.Close(websocket.StatusTryAgainLater, "upstream error")
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// watchServices sends the visible service list now and after every change.
func (a *api) watchServices(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	release, ok := a.watches.acquire(p.Subject)
	if !ok {
		writeError(w, r, http.StatusTooManyRequests)
		return
	}
	defer release()

	changes, unsubscribe := a.deps.Workloads.Subscribe()
	defer unsubscribe()

	conn, ok := a.acceptWebSocket(w, r)
	if !ok {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	ctx, principals, cancel := a.streamContext(r, conn)
	defer cancel()

	var last []byte
	send := func() error {
		data, err := json.Marshal(map[string]any{"services": a.visibleServices(p, "")})
		if err != nil {
			return err
		}
		if bytes.Equal(data, last) {
			return nil
		}
		last = data
		wctx, wcancel := context.WithTimeout(ctx, wsWriteTimeout)
		defer wcancel()
		return conn.Write(wctx, websocket.MessageText, data)
	}

	refresh := time.NewTicker(watchRefresh)
	defer refresh.Stop()
	var debounce <-chan time.Time
	if err := send(); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			if debounce == nil {
				debounce = time.After(watchDebounce)
			}
			continue
		case np := <-principals:
			p = np
		case <-refresh.C:
		case <-debounce:
			debounce = nil
		}
		if err := send(); err != nil {
			return
		}
	}
}
