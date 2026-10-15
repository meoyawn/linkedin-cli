package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPFailureDiagnostics(t *testing.T) {
	for _, outcome := range []string{"rejected", "timeout", "large response", "truncated JSON"} {
		t.Run(outcome, func(t *testing.T) {
			calls := 0
			client := linkedinClient{session: session{LIAT: "private-cookie-value", JSessionID: "private-csrf-value"}}
			client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if outcome == "timeout" {
					return nil, context.DeadlineExceeded
				}
				body := `{"message":"InMail content required","serviceErrorCode":42,"access_token":"rotated-secret","echo":"private-cookie-value private-csrf-value"}`
				if outcome == "large response" {
					body = strings.Repeat("x", 100000)
				}
				if outcome == "truncated JSON" {
					body = `{"access_token":"rotated-secret","padding":"` + strings.Repeat("x", 100000) + `"}`
				}
				return &http.Response{StatusCode: 422, Header: http.Header{"Content-Type": {"application/json"}, "X-Li-Uuid": {"support-request-id"}, "Set-Cookie": {"another-private-cookie"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			request := `{"message":{"body":{"text":"private approved message"}},"password":"request-secret"}`
			if outcome == "truncated JSON" {
				request = `{"text":"private approved message` + strings.Repeat("x", 100000) + `"}`
			}
			_, err := client.request(context.Background(), http.MethodPost, "/voyager/api/example?action=createMessage&access_token=query-secret", strings.NewReader(request), http.Header{"Content-Type": {"text/plain;charset=UTF-8"}})
			if err == nil || calls != 1 {
				t.Fatalf("failure missing or retried: calls=%d err=%v", calls, err)
			}
			diagnostic := err.Error()
			for _, want := range []string{"HTTP diagnostics", "POST", "/voyager/api/example", "createMessage", "elapsed_ms", "Content-Type"} {
				if !strings.Contains(diagnostic, want) {
					t.Errorf("diagnostic missing %q: %s", want, diagnostic)
				}
			}
			for _, secret := range []string{"private-cookie-value", "private-csrf-value", "rotated-secret", "another-private-cookie", "query-secret", "request-secret", "private approved message"} {
				if strings.Contains(diagnostic, secret) {
					t.Errorf("diagnostic leaked %q", secret)
				}
			}
			switch outcome {
			case "rejected":
				for _, want := range []string{"422", "support-request-id", "InMail content required", "serviceErrorCode"} {
					if !strings.Contains(diagnostic, want) {
						t.Errorf("response detail missing %q: %s", want, diagnostic)
					}
				}
			case "timeout":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("lost transport error: %v", err)
				}
			case "large response", "truncated JSON":
				if len(diagnostic) > 25000 || !strings.Contains(diagnostic, "truncated") {
					t.Errorf("unbounded or unmarked response preview: length=%d", len(diagnostic))
				}
			}
		})
	}
}
