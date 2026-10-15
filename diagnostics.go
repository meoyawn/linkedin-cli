package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const diagnosticBodyLimit = 16 << 10

var diagnosticCredential = regexp.MustCompile(`(?i)((?:authorization|cookie|csrf[-_]?token|li_at|jsessionid|access[-_]?token|refresh[-_]?token|password|secret)["']?\s*[=:]\s*["']?)[^\r\n;,<>&"'()]+`)

type diagnosticError struct {
	cause  error
	detail string
}

func (e *diagnosticError) Error() string { return e.detail }
func (e *diagnosticError) Unwrap() error { return e.cause }

func diagnosticSecretKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(key))
	return key != "origintoken" && (strings.Contains(key, "token") || strings.Contains(key, "password") || strings.Contains(key, "secret") || strings.Contains(key, "cookie") || key == "authorization" || key == "jsessionid" || key == "liat")
}

func (c *linkedinClient) redactDiagnostic(text string) string {
	for _, secret := range []string{c.session.LIAT, c.session.JSessionID} {
		if secret == "" {
			continue
		}
		encoded, _ := json.Marshal(secret)
		for _, value := range []string{secret, url.QueryEscape(secret), strings.Trim(string(encoded), `"`)} {
			text = strings.ReplaceAll(text, value, "[redacted]")
		}
	}
	return diagnosticCredential.ReplaceAllString(text, "${1}[redacted]")
}

func (c *linkedinClient) diagnosticURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return c.redactDiagnostic(raw)
	}
	u.User = nil
	query := u.Query()
	for key := range query {
		if diagnosticSecretKey(key) {
			query.Set(key, "[redacted]")
		}
	}
	u.RawQuery = query.Encode()
	return c.redactDiagnostic(u.String())
}

func (c *linkedinClient) diagnosticBody(body io.ReadCloser, request bool) any {
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, diagnosticBodyLimit+1))
	truncated := len(raw) > diagnosticBodyLimit
	if truncated {
		raw = raw[:diagnosticBodyLimit]
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		value = c.redactDiagnostic(strings.ToValidUTF8(string(raw), "�"))
		if request {
			value = "[request body omitted: incomplete or non-JSON content]"
		}
	} else {
		var scrub func(any) any
		scrub = func(v any) any {
			switch v := v.(type) {
			case map[string]any:
				for key, child := range v {
					if diagnosticSecretKey(key) || key == "text" || key == "subject" {
						v[key] = "[redacted]"
					} else {
						v[key] = scrub(child)
					}
				}
			case []any:
				for i, child := range v {
					v[i] = scrub(child)
				}
			case string:
				return c.redactDiagnostic(v)
			}
			return v
		}
		value = scrub(value)
	}
	return map[string]any{"preview": value, "truncated": truncated, "read_error": errString(err)}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Only diagnostics for the failed request are returned. Cookies and auth
// headers are never captured; message text and credential fields are redacted.
func (c *linkedinClient) requestFailure(req *http.Request, resp *http.Response, started time.Time, cause error) error {
	safeURL := c.diagnosticURL(req.URL.String())
	detail := map[string]any{
		"method": req.Method, "url": safeURL, "started_at": started.UTC().Format(time.RFC3339Nano),
		"elapsed_ms": time.Since(started).Milliseconds(), "request_headers": map[string]string{"Accept": req.Header.Get("Accept"), "Content-Type": req.Header.Get("Content-Type"), "X-Restli-Protocol-Version": req.Header.Get("X-Restli-Protocol-Version")},
	}
	if req.GetBody != nil {
		if body, err := req.GetBody(); err == nil {
			detail["request_body"] = c.diagnosticBody(body, true)
		}
	}
	if resp != nil {
		detail["status"] = resp.StatusCode
		headers := map[string]string{}
		for _, key := range []string{"Content-Type", "Retry-After", "X-Li-Uuid", "X-Li-Responseorigin", "X-Restli-Error-Response", "Location"} {
			if value := resp.Header.Get(key); value != "" {
				if key == "Location" {
					value = c.diagnosticURL(value)
				}
				headers[key] = c.redactDiagnostic(value)
			}
		}
		detail["response_headers"] = headers
		detail["response_body"] = c.diagnosticBody(resp.Body, false)
	}
	encoded, _ := json.MarshalIndent(detail, "", "  ")
	summary := c.redactDiagnostic(strings.ReplaceAll(cause.Error(), req.URL.String(), safeURL))
	return &diagnosticError{cause: cause, detail: fmt.Sprintf("%s\nHTTP diagnostics:\n%s", summary, encoded)}
}
