// Package logs queries Loki. Every query starts from an authz.LogSelector;
// the client only appends line filters built from validated parameters, so
// no LogQL ever comes from the browser (CLAUDE.md rules 1-3).
package logs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/R0cker64/kubeportal/backend/internal/authz"
)

// Levels accepted by the level filter, matched against Loki's detected_level.
var Levels = []string{"debug", "info", "warn", "error"}

const (
	maxResponseBytes = 32 << 20
	// maxLineBytes truncates very long lines before they reach the browser.
	maxLineBytes  = 16 << 10
	maxTailFrame  = 4 << 20
	tailBatchSize = 100
)

// ErrUpstream wraps every Loki failure. Callers return a generic error.
var ErrUpstream = errors.New("logs: upstream error")

// Filter narrows a selector. Text is matched as a literal substring.
type Filter struct {
	Text  string
	Level string // "" or one of Levels
}

// Line is one log line.
type Line struct {
	Time  time.Time `json:"time"`
	Pod   string    `json:"pod"`
	Level string    `json:"level,omitempty"`
	Line  string    `json:"line"`
}

// Client talks to the Loki HTTP API.
type Client struct {
	base    *url.URL
	http    *http.Client // with timeout, for query_range
	ws      *http.Client // without timeout, for the long-lived tail
	timeout time.Duration
}

// NewClient returns a client for the Loki at base. timeout bounds every
// query and the tail handshake.
func NewClient(base *url.URL, timeout time.Duration) *Client {
	return &Client{
		base:    base,
		http:    &http.Client{Timeout: timeout},
		ws:      &http.Client{},
		timeout: timeout,
	}
}

// BuildQuery returns the LogQL for sel and f. The text is quoted as a LogQL
// string literal (Go syntax) and the level must be in the allowlist.
func BuildQuery(sel authz.LogSelector, f Filter) (string, error) {
	q := sel.String()
	if q == "" {
		return "", authz.ErrInvalidParam
	}
	if f.Text != "" {
		q += " |= " + strconv.Quote(f.Text)
	}
	if f.Level != "" {
		if !slices.Contains(Levels, f.Level) {
			return "", authz.ErrInvalidParam
		}
		q += " | detected_level=" + strconv.Quote(f.Level)
	}
	return q, nil
}

// Query returns up to limit lines in r, newest first.
func (c *Client) Query(ctx context.Context, sel authz.LogSelector, f Filter, r authz.TimeRange, limit int) ([]Line, error) {
	q, err := BuildQuery(sel, f)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	u := c.base.JoinPath("/loki/api/v1/query_range")
	params := url.Values{}
	params.Set("query", q)
	params.Set("start", strconv.FormatInt(r.Start.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(r.End.UnixNano(), 10))
	params.Set("limit", strconv.Itoa(limit))
	params.Set("direction", "backward")
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrUpstream, resp.StatusCode)
	}

	var body struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string   `json:"resultType"`
			Result     []stream `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: decode: %w", ErrUpstream, err)
	}
	if body.Status != "success" || body.Data.ResultType != "streams" {
		return nil, fmt.Errorf("%w: unexpected result %q", ErrUpstream, body.Data.ResultType)
	}
	lines := flatten(body.Data.Result)
	slices.SortStableFunc(lines, func(a, b Line) int { return b.Time.Compare(a.Time) })
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return lines, nil
}

// Tail streams new lines matching sel and f to emit until ctx ends, emit
// fails or Loki closes the stream.
func (c *Client) Tail(ctx context.Context, sel authz.LogSelector, f Filter, emit func([]Line) error) error {
	q, err := BuildQuery(sel, f)
	if err != nil {
		return err
	}
	u := c.base.JoinPath("/loki/api/v1/tail")
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	params := url.Values{}
	params.Set("query", q)
	params.Set("limit", strconv.Itoa(tailBatchSize))
	u.RawQuery = params.Encode()

	dialCtx, cancel := context.WithTimeout(ctx, c.timeout)
	conn, resp, err := websocket.Dial(dialCtx, u.String(), &websocket.DialOptions{HTTPClient: c.ws})
	cancel()
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("%w: tail: %w", ErrUpstream, err)
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(maxTailFrame)

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("%w: tail read: %w", ErrUpstream, err)
		}
		var msg struct {
			Streams []stream `json:"streams"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			return fmt.Errorf("%w: tail decode: %w", ErrUpstream, err)
		}
		lines := flatten(msg.Streams)
		if len(lines) == 0 {
			continue
		}
		slices.SortStableFunc(lines, func(a, b Line) int { return a.Time.Compare(b.Time) })
		if err := emit(lines); err != nil {
			return err
		}
	}
}

type stream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

func flatten(streams []stream) []Line {
	var out []Line
	for _, s := range streams {
		for _, v := range s.Values {
			ns, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				continue
			}
			out = append(out, Line{
				Time:  time.Unix(0, ns).UTC(),
				Pod:   s.Stream["pod"],
				Level: s.Stream["detected_level"],
				Line:  truncate(strings.TrimRight(v[1], "\n")),
			})
		}
	}
	return out
}

func truncate(s string) string {
	if len(s) <= maxLineBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxLineBytes], "") + "…"
}
