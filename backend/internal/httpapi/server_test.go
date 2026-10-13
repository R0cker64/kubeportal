package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/R0cker64/kubeportal/backend/internal/config"
)

func testConfig(publicURL string) *config.Config {
	u, _ := url.Parse(publicURL)
	return &config.Config{PublicURL: u, MaxBodyBytes: 16}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func do(t *testing.T, h http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, target, body))
	return rec
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	h := NewHandler(testConfig("http://localhost:8080"), testLogger(), nil)

	for _, target := range []string{"/healthz", "/api/unknown", "/nope"} {
		rec := do(t, h, http.MethodGet, target, nil)
		csp := rec.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe-") {
			t.Errorf("%s: bad CSP %q", target, csp)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", target)
		}
		if rec.Header().Get("Referrer-Policy") == "" {
			t.Errorf("%s: missing Referrer-Policy", target)
		}
		if rec.Header().Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: HSTS must not be set without TLS", target)
		}
		if rec.Header().Get(requestIDHeader) == "" {
			t.Errorf("%s: missing request id", target)
		}
	}
}

func TestHSTSWithTLS(t *testing.T) {
	h := NewHandler(testConfig("https://portal.example.com"), testLogger(), nil)
	if do(t, h, http.MethodGet, "/healthz", nil).Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("expected HSTS header when public URL uses https")
	}
}

func TestErrorsAreGenericWithCorrelationID(t *testing.T) {
	h := NewHandler(testConfig("http://localhost:8080"), testLogger(), nil)
	rec := do(t, h, http.MethodGet, "/api/unknown", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	var body errorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "Not Found" || body.RequestID != rec.Header().Get(requestIDHeader) {
		t.Fatalf("unexpected body %+v", body)
	}
}

func TestIncomingRequestIDIsIgnored(t *testing.T) {
	h := NewHandler(testConfig("http://localhost:8080"), testLogger(), nil)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
	req.Header.Set(requestIDHeader, "injected\nvalue")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get(requestIDHeader); got == "injected\nvalue" || len(got) != 32 {
		t.Fatalf("request id not server-generated: %q", got)
	}
}

func TestPanicReturnsGeneric500(t *testing.T) {
	h := withRequestID(withRecover(testLogger(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret internal detail")
	})))
	rec := do(t, h, http.MethodGet, "/", nil)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestMaxBodyIsEnforced(t *testing.T) {
	cfg := testConfig("http://localhost:8080")
	h := withMaxBody(cfg.MaxBodyBytes, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		}
	}))
	rec := do(t, h, http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 64)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestSPAHandler(t *testing.T) {
	static := fstest.MapFS{
		"index.html":      {Data: []byte(`<html><app-root ngCspNonce="__CSP_NONCE__">app</app-root></html>`)},
		"main-ABC.js":     {Data: []byte("console.log(1)")},
		"assets/logo.svg": {Data: []byte("<svg/>")},
	}
	h := NewHandler(testConfig("http://localhost:8080"), testLogger(), static)

	cases := []struct {
		target string
		status int
		body   string
	}{
		{"/", http.StatusOK, "app"},
		{"/teams/payments", http.StatusOK, "app"},
		{"/assets", http.StatusOK, "app"},
		{"/main-ABC.js", http.StatusOK, "console.log"},
		{"/missing.js", http.StatusNotFound, "Not Found"},
		{"/api/unknown", http.StatusNotFound, "Not Found"},
		// The mux normalizes traversal attempts into a redirect to the clean path.
		{"/../../etc/passwd", http.StatusTemporaryRedirect, `href="/etc/passwd"`},
		{"/etc/passwd", http.StatusOK, "app"},
	}
	for _, c := range cases {
		rec := do(t, h, http.MethodGet, c.target, nil)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s: got %d %q", c.target, rec.Code, rec.Body.String())
		}
	}
}

func TestIndexGetsPerRequestNonce(t *testing.T) {
	static := fstest.MapFS{"index.html": {Data: []byte(`<app-root ngCspNonce="__CSP_NONCE__"></app-root>`)}}
	h := NewHandler(testConfig("http://localhost:8080"), testLogger(), static)

	first := do(t, h, http.MethodGet, "/", nil)
	second := do(t, h, http.MethodGet, "/", nil)

	nonce := func(rec *httptest.ResponseRecorder) string {
		csp := rec.Header().Get("Content-Security-Policy")
		_, after, ok := strings.Cut(csp, "'nonce-")
		if !ok {
			t.Fatalf("CSP without nonce: %q", csp)
		}
		n, _, _ := strings.Cut(after, "'")
		return n
	}
	n1, n2 := nonce(first), nonce(second)
	if n1 == n2 {
		t.Fatal("nonce must change on every request")
	}
	if want := `ngCspNonce="` + n1 + `"`; !strings.Contains(first.Body.String(), want) {
		t.Fatalf("body %q does not contain %s", first.Body.String(), want)
	}
	if strings.Contains(first.Body.String(), "__CSP_NONCE__") {
		t.Fatal("placeholder leaked")
	}
}
