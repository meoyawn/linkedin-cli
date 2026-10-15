package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Exercise SweetCookie's real JSON importer and the CLI's complete lifecycle;
// no browser access or HTTP is needed for cookie management.
func TestAuthCookieLifecycle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LINKEDIN_CONFIG_DIR", filepath.Join(dir, "config"))
	file := filepath.Join(dir, "input.json")
	if err := os.WriteFile(file, []byte(`[
		{"name":"li_at","value":"secret-token","domain":".linkedin.com","path":"/","secure":true},
		{"name":"JSESSIONID","value":"\"ajax:test\"","domain":".linkedin.com","path":"/","secure":true},
		{"name":"unrelated","value":"must-not-be-saved","domain":".linkedin.com","path":"/"}
	]`), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	invoke := func(args ...string) authStatus {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if err := run(context.Background(), args, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stdout.String(), "secret-token") || strings.Contains(stdout.String(), "ajax:test") {
			t.Fatal("cookie value leaked to stdout")
		}
		var status authStatus
		if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	if invoke("auth", "status").Authenticated {
		t.Fatal("fresh config was authenticated")
	}
	if status := invoke("auth", "login", "--cookies-file", file); !status.Authenticated || status.Source != "inline" {
		t.Fatalf("import status = %+v", status)
	}
	store := authStore{dir: os.Getenv("LINKEDIN_CONFIG_DIR")}
	saved, err := store.load()
	if err != nil || saved.LIAT != "secret-token" || saved.JSessionID != "ajax:test" {
		t.Fatalf("saved session differs from imported cookies: %v", err)
	}
	info, err := os.Stat(store.path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("cookie file permissions = %o; want 600", info.Mode().Perm())
	}
	if !invoke("auth", "status").Authenticated {
		t.Fatal("saved cookies were not recognized")
	}
	if invoke("auth", "logout").Authenticated || invoke("auth", "status").Authenticated {
		t.Fatal("logout retained cookies")
	}
}

func TestInvalidInputsFailBeforeAuthentication(t *testing.T) {
	t.Setenv("LINKEDIN_CONFIG_DIR", t.TempDir())
	for _, args := range [][]string{
		{"companies", "people", "example-company", "--limit", "0"},
		{"companies", "people", "example-company", "--start=-1"},
		{"companies", "people", "https://example.com/company/test"},
		{"companies", "people", "example-company,other"},
		{"people", "profile", "https://example.com/in/person"},
		{"people", "profile", "urn:li:fsd_profile:bad,id"},
		{"people", "profile", "https://www.linkedin.com/in/example-person/posts/"},
		{"people", "mutuals", "example-person", "--limit=0"},
		{"people", "mutuals", "example-person", "--limit=11"},
		{"people", "mutuals", "example-person,other"},
		{"auth", "login", "--browser", "unknown"},
		{"auth", "login", "--cookies-file", "file", "--browser", "chrome"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := run(context.Background(), args, &out, &errOut)
			if err == nil || strings.Contains(err.Error(), "no saved cookies") || out.Len() != 0 {
				t.Fatalf("invalid input was not rejected before auth: %v; stdout=%s", err, out.String())
			}
		})
	}
}
