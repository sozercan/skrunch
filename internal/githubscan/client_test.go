package githubscan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v69/github"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func bufferDebugf(buf *bytes.Buffer) func(string, ...any) {
	return func(format string, args ...any) {
		if !strings.HasSuffix(format, "\n") {
			format += "\n"
		}
		fmt.Fprintf(buf, format, args...)
	}
}

func TestNewAuthenticatedHTTPClientDoesNotExposeCancelRequest(t *testing.T) {
	client := newAuthenticatedHTTPClient("test-token")

	type cancelRequester interface {
		CancelRequest(*http.Request)
	}

	if _, ok := client.Transport.(cancelRequester); ok {
		t.Fatal("authenticated client transport unexpectedly implements CancelRequest")
	}
}

func TestStaticTokenTransportAddsBearerTokenWithoutMutatingRequest(t *testing.T) {
	transport := &staticTokenTransport{
		token: "test-token",
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got, want := req.Header.Get("Authorization"), "Bearer test-token"; got != want {
				t.Fatalf("RoundTrip() Authorization header = %q, want %q", got, want)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       http.NoBody,
				Request:    req,
			}, nil
		}),
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("RoundTrip() status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("RoundTrip() mutated original request Authorization header = %q", got)
	}
}

func TestWithAnonymousFallbackOnSAMLError(t *testing.T) {
	primary := &github.Client{}
	anonymous := &github.Client{}
	var buf bytes.Buffer
	client := &Client{
		api:       primary,
		anonymous: anonymous,
		debugLogf: bufferDebugf(&buf),
	}

	var calls []string
	resp, err := client.withAnonymousFallback(context.Background(), "get repository octo/repo", func(_ context.Context, current *github.Client) (*github.Response, error) {
		switch current {
		case primary:
			calls = append(calls, "primary")
			httpResp := &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     http.Header{"X-GitHub-SSO": []string{"required; url=https://github.com/orgs/github/sso"}},
			}
			return &github.Response{Response: httpResp}, &github.ErrorResponse{
				Response: httpResp,
				Message:  "Resource protected by organization SAML enforcement.",
			}
		case anonymous:
			calls = append(calls, "anonymous")
			httpResp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}
			return &github.Response{Response: httpResp}, nil
		default:
			t.Fatalf("unexpected client pointer: %p", current)
			return nil, nil
		}
	})
	if err != nil {
		t.Fatalf("withAnonymousFallback() error = %v", err)
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("withAnonymousFallback() status = %v, want %d", statusCode(resp), http.StatusOK)
	}
	if len(calls) != 2 || calls[0] != "primary" || calls[1] != "anonymous" {
		t.Fatalf("withAnonymousFallback() calls = %v, want [primary anonymous]", calls)
	}
	if got := buf.String(); !strings.Contains(got, "get repository octo/repo: falling back to anonymous client after SAML enforcement\n") {
		t.Fatalf("withAnonymousFallback() debug log = %q, want fallback message", got)
	}
	if got := buf.String(); !strings.Contains(got, "get repository octo/repo: anonymous fallback succeeded\n") {
		t.Fatalf("withAnonymousFallback() debug log = %q, want success message", got)
	}
}

func TestWithAnonymousFallbackReturnsFallbackErrorWhenFallbackFails(t *testing.T) {
	primary := &github.Client{}
	anonymous := &github.Client{}
	client := &Client{
		api:       primary,
		anonymous: anonymous,
	}

	primaryResp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"X-GitHub-SSO": []string{"required"}},
	}
	primaryErr := &github.ErrorResponse{
		Response: primaryResp,
		Message:  "Resource protected by organization SAML enforcement.",
	}
	fallbackErr := errors.New("anonymous lookup failed")

	var calls []string
	resp, err := client.withAnonymousFallback(context.Background(), "get repository octo/repo", func(_ context.Context, current *github.Client) (*github.Response, error) {
		switch current {
		case primary:
			calls = append(calls, "primary")
			return &github.Response{Response: primaryResp}, primaryErr
		case anonymous:
			calls = append(calls, "anonymous")
			httpResp := &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header)}
			return &github.Response{Response: httpResp}, fallbackErr
		default:
			t.Fatalf("unexpected client pointer: %p", current)
			return nil, nil
		}
	})
	if !errors.Is(err, fallbackErr) {
		t.Fatalf("withAnonymousFallback() error = %v, want fallback error %v", err, fallbackErr)
	}
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("withAnonymousFallback() status = %v, want %d", statusCode(resp), http.StatusNotFound)
	}
	if len(calls) != 2 || calls[0] != "primary" || calls[1] != "anonymous" {
		t.Fatalf("withAnonymousFallback() calls = %v, want [primary anonymous]", calls)
	}
	if got := err.Error(); !strings.Contains(got, "authenticated request hit SAML enforcement; anonymous fallback failed") {
		t.Fatalf("withAnonymousFallback() error = %q, want combined message", got)
	}
}

func TestWithAnonymousFallbackSkipsNonSAMLErrors(t *testing.T) {
	primary := &github.Client{}
	anonymous := &github.Client{}
	client := &Client{
		api:       primary,
		anonymous: anonymous,
	}

	expectedErr := errors.New("plain forbidden")
	httpResp := &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header)}

	var calls []string
	resp, err := client.withAnonymousFallback(context.Background(), "get repository octo/repo", func(_ context.Context, current *github.Client) (*github.Response, error) {
		switch current {
		case primary:
			calls = append(calls, "primary")
			return &github.Response{Response: httpResp}, expectedErr
		case anonymous:
			calls = append(calls, "anonymous")
			return nil, nil
		default:
			t.Fatalf("unexpected client pointer: %p", current)
			return nil, nil
		}
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("withAnonymousFallback() error = %v, want %v", err, expectedErr)
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("withAnonymousFallback() status = %v, want %d", statusCode(resp), http.StatusForbidden)
	}
	if len(calls) != 1 || calls[0] != "primary" {
		t.Fatalf("withAnonymousFallback() calls = %v, want [primary]", calls)
	}
}

func TestWithRetryClientLogsRetryDelay(t *testing.T) {
	oldSleepContextFn := sleepContextFn
	sleepContextFn = func(ctx context.Context, delay time.Duration) error {
		if got, want := delay, apiRetryBaseDelay; got != want {
			t.Fatalf("sleepContextFn() delay = %s, want %s", got, want)
		}
		return nil
	}
	defer func() {
		sleepContextFn = oldSleepContextFn
	}()

	var buf bytes.Buffer
	client := &Client{
		debugLogf: bufferDebugf(&buf),
	}

	attempts := 0
	resp, err := client.withRetryClient(
		context.Background(),
		&github.Client{},
		"compare commits for octo/repo (refs/heads/main..deadbeef)",
		func(_ context.Context, _ *github.Client) (*github.Response, error) {
			attempts++
			if attempts == 1 {
				httpResp := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header)}
				return &github.Response{Response: httpResp}, errors.New("temporary failure")
			}

			httpResp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}
			return &github.Response{Response: httpResp}, nil
		},
	)
	if err != nil {
		t.Fatalf("withRetryClient() error = %v", err)
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("withRetryClient() status = %v, want %d", statusCode(resp), http.StatusOK)
	}
	if got, want := attempts, 2; got != want {
		t.Fatalf("withRetryClient() attempts = %d, want %d", got, want)
	}

	got := buf.String()
	if !strings.Contains(got, "compare commits for octo/repo (refs/heads/main..deadbeef): retrying in 500ms after attempt 1/5 failed") {
		t.Fatalf("withRetryClient() debug log = %q, want retry line", got)
	}
	if !strings.Contains(got, "status=503: temporary failure") {
		t.Fatalf("withRetryClient() debug log = %q, want status and error details", got)
	}
}

func TestRetryDelaySkipsLongRateLimitReset(t *testing.T) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.github.com/repos/octo/repo/commits/deadbeef", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}

	rateLimitErr := &github.RateLimitError{
		Rate: github.Rate{
			Reset: github.Timestamp{Time: time.Now().Add(time.Minute)},
		},
		Response: &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Request:    req,
		},
		Message: "rate limit exceeded",
	}

	delay, ok := retryDelay(
		&github.Response{
			Response: rateLimitErr.Response,
			Rate:     rateLimitErr.Rate,
		},
		rateLimitErr,
		0,
	)
	if ok {
		t.Fatalf("retryDelay() ok = %t, want false (delay=%s)", ok, delay)
	}
	if delay != 0 {
		t.Fatalf("retryDelay() delay = %s, want 0", delay)
	}
}
