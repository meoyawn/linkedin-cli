package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
)

type session struct {
	LIAT       string     `json:"li_at"`
	JSessionID string     `json:"jsessionid"`
	Source     string     `json:"source"`
	Profile    string     `json:"profile,omitempty"`
	ImportedAt time.Time  `json:"imported_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

type authStatus struct {
	Authenticated bool       `json:"authenticated"`
	Source        string     `json:"source,omitempty"`
	Profile       string     `json:"profile,omitempty"`
	ImportedAt    *time.Time `json:"imported_at,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Reason        string     `json:"reason,omitempty"`
}

func (s session) validate() error {
	for _, c := range []*http.Cookie{{Name: "li_at", Value: s.LIAT}, {Name: "JSESSIONID", Value: s.JSessionID}} {
		if c.Value == "" || c.Valid() != nil {
			return fmt.Errorf("missing or invalid %s cookie; run linkedin auth login", c.Name)
		}
	}
	if s.ExpiresAt != nil && !s.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("saved cookies expired at %s; run linkedin auth login", s.ExpiresAt.Format(time.RFC3339))
	}
	return nil
}

func (s session) status() authStatus {
	status := authStatus{Source: s.Source, Profile: s.Profile, ImportedAt: &s.ImportedAt, ExpiresAt: s.ExpiresAt}
	if err := s.validate(); err != nil {
		status.Reason = err.Error()
	} else {
		status.Authenticated = true
	}
	return status
}

type authStore struct{ dir string }

func (s authStore) path() string { return filepath.Join(s.dir, "cookies.json") }

func (s authStore) read() (session, error) {
	f, err := os.Open(s.path())
	if err != nil {
		return session{}, err
	}
	defer f.Close()
	var result session
	if err := json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&result); err != nil {
		return session{}, fmt.Errorf("invalid saved cookie file; run linkedin auth login")
	}
	return result, nil
}

func (s authStore) load() (session, error) {
	result, err := s.read()
	if errors.Is(err, os.ErrNotExist) {
		return session{}, fmt.Errorf("no saved cookies; run linkedin auth login")
	}
	if err != nil {
		return session{}, fmt.Errorf("load saved cookies: %w", err)
	}
	if err := result.validate(); err != nil {
		return session{}, err
	}
	return result, nil
}

func (s authStore) save(value session) error {
	if err := value.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return fmt.Errorf("create cookie directory: %w", err)
	}
	if err := os.Chmod(s.dir, 0700); err != nil {
		return fmt.Errorf("protect cookie directory: %w", err)
	}
	f, err := os.CreateTemp(s.dir, ".cookies-*")
	if err != nil {
		return fmt.Errorf("create cookie file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(value); err != nil {
		return fmt.Errorf("save cookies: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close cookie file: %w", err)
	}
	if err := os.Rename(f.Name(), s.path()); err != nil {
		return fmt.Errorf("replace cookie file: %w", err)
	}
	return nil
}

func importSession(ctx context.Context, browsers []sweetcookie.Browser, profile, file string) (session, []string, error) {
	if file != "" {
		browsers = []sweetcookie.Browser{sweetcookie.BrowserInline}
	}
	var warnings []string
	for _, browser := range browsers {
		if err := ctx.Err(); err != nil {
			return session{}, warnings, err
		}
		result, err := sweetcookie.Get(ctx, sweetcookie.Options{
			URL:   "https://www.linkedin.com/voyager/api/graphql",
			Names: []string{"li_at", "JSESSIONID"}, Browsers: []sweetcookie.Browser{browser},
			Profiles: map[sweetcookie.Browser]string{browser: profile},
			Inline:   sweetcookie.InlineCookies{File: file}, Mode: sweetcookie.ModeMerge,
			Timeout: 5 * time.Second,
		})
		warnings = append(warnings, result.Warnings...)
		if err != nil {
			return session{}, warnings, fmt.Errorf("read LinkedIn cookies with SweetCookie: %w", err)
		}
		// Never combine authentication cookies from different profiles/containers.
		type sourceKey struct {
			source    sweetcookie.Source
			container int
		}
		groups := make(map[sourceKey]*session)
		var order []sourceKey
		for _, cookie := range result.Cookies {
			key := sourceKey{source: cookie.Source, container: cookie.Container.ID}
			s := groups[key]
			if s == nil {
				s = &session{Source: string(cookie.Source.Browser), Profile: cookie.Source.Profile, ImportedAt: time.Now().UTC()}
				groups[key] = s
				order = append(order, key)
			}
			switch cookie.Name {
			case "li_at":
				s.LIAT = cookie.Value
			case "JSESSIONID":
				s.JSessionID = strings.Trim(cookie.Value, `"`)
			}
			if cookie.Expires != nil && (s.ExpiresAt == nil || cookie.Expires.Before(*s.ExpiresAt)) {
				s.ExpiresAt = cookie.Expires
			}
		}
		for _, key := range order {
			if s := groups[key]; s.validate() == nil {
				return *s, warnings, nil
			}
		}
	}
	return session{}, warnings, fmt.Errorf("no usable LinkedIn session found: li_at and JSESSIONID must come from the same profile; sign in at linkedin.com, then run linkedin auth login --browser <browser> [--profile <profile>]")
}
