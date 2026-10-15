package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Synthetic markup retains the captured SDUI structure, including nested links
// and related people inside cards that must not become introduction candidates.
func TestMutualsReadsCapturedDocumentRequestAndOnlyPrimaryResults(t *testing.T) {
	body, err := os.ReadFile("testdata/mutuals.html")
	if err != nil {
		t.Fatal(err)
	}
	profileBody, err := os.ReadFile("testdata/mutuals-profile.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"urn:li:fsd_profile:ACoExamplePerson", "example-person"} {
		t.Run(input, func(t *testing.T) {
			calls := 0
			client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path == "/voyager/api/identity/dash/profiles" && calls == 1 {
					return jsonResponse(fixture(t, "profile")), nil
				}
				if r.URL.Path == "/in/example-person/" && calls == 2 && r.Header.Get("Accept") == "text/html" {
					return jsonResponse(profileBody), nil
				}
				q := r.URL.Query()
				if r.Method != http.MethodGet || r.URL.Path != "/search/results/people/" || r.Header.Get("Accept") != "text/html" ||
					q.Get("origin") != "MEMBER_PROFILE_CANNED_SEARCH" || q.Get("network") != `["F"]` || q.Get("connectionOf") != `["ACoExamplePerson"]` || len(q) != 3 {
					t.Fatalf("request does not match the captured mutual view: %s %s", r.Method, r.URL)
				}
				return jsonResponse(body), nil
			})}}
			ref, err := parseProfile(input)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.mutuals(context.Background(), ref, 3)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 3 || result.Returned != 3 || !result.HasMore || len(result.People) != 3 || !result.Verified || result.Profile.ID != "ACoExamplePerson" || result.Profile.URL != "https://www.linkedin.com/in/example-person/" || result.ProfileEvidence != "First Mutual and other mutual connections" {
				t.Fatalf("unexpected budget or results: calls=%d result=%+v", calls, result)
			}
			for i, p := range result.People {
				wantName := []string{"First Mutual", "Second Mutual", "Third Mutual"}[i]
				wantSlug := []string{"first-mutual", "second-mutual", "third-mutual"}[i]
				if p.Name != wantName || p.URL != "https://www.linkedin.com/in/"+wantSlug+"/" || p.ConnectionDegree != "1st" {
					t.Fatalf("non-primary or incorrectly parsed result: %+v", p)
				}
			}
			if p := result.People[0]; p.Headline != "Senior Go & Platform Engineer" || p.Location != "Test City" || p.ProfileURN != "urn:li:fsd_profile:ACoFirstMutual" {
				t.Fatalf("incorrect mutual fields: %+v", p)
			}
			if p := result.People[2]; p.Headline != "" || p.Location != "" {
				t.Fatalf("invented optional profile fields: %+v", p)
			}
		})
	}
}

func TestMutualsFailsClosedWithoutRetryingOrEmittingPartialPeople(t *testing.T) {
	body, err := os.ReadFile("testdata/mutuals.html")
	if err != nil {
		t.Fatal(err)
	}
	profileBody, err := os.ReadFile("testdata/mutuals-profile.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"rate limit", "", "rate limit", 429},
		{"login", "<html><form>Sign in</form></html>", "checkpoint", 200},
		{"changed", `<div data-sdui-screen="com.linkedin.sdui.flagshipnav.search.SearchResultsPeople"></div>`, "no recognizable", 200},
		{"wrong scope", strings.Replace(string(body), "Second Mutual<span> • 1st", "Second Mutual<span> • 2nd", 1), "first-degree", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path == "/voyager/api/identity/dash/profiles" && calls == 1 {
					return jsonResponse(fixture(t, "profile")), nil
				}
				if r.URL.Path == "/in/example-person/" && calls == 2 {
					return jsonResponse(profileBody), nil
				}
				if r.URL.Path != "/search/results/people/" || calls != 3 {
					t.Fatalf("unexpected request: %s", r.URL)
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			ref, err := parseProfile("urn:li:fsd_profile:ACoExamplePerson")
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.mutuals(context.Background(), ref, 10)
			if err == nil || !strings.Contains(err.Error(), tc.want) || calls != 3 || len(result.People) != 0 || result.Verified {
				t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
			}
		})
	}
}

func TestMutualsRejectsUnboundConnectionLists(t *testing.T) {
	results, err := os.ReadFile("testdata/mutuals.html")
	if err != nil {
		t.Fatal(err)
	}
	profileBody, err := os.ReadFile("testdata/mutuals-profile.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, profile string }{
		{"echoed query only", `<main><h1>Example Person</h1><p>2nd</p><script type="application/json">{"initialParams":{"connectionOf":"[\"ACoExamplePerson\"]","network":"[\"F\"]"}}</script></main>`},
		{"unlabelled search link", strings.Replace(string(profileBody), "First Mutual and other mutual connections", "Connections", 1)},
		{"another target", strings.ReplaceAll(string(profileBody), "ACoExamplePerson", "ACoAnotherPerson")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			searches := 0
			client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/voyager/api/identity/dash/profiles":
					return jsonResponse(fixture(t, "profile")), nil
				case "/in/example-person/":
					return jsonResponse([]byte(tc.profile)), nil
				case "/search/results/people/":
					searches++
					return jsonResponse(results), nil
				default:
					t.Fatalf("unexpected request: %s", r.URL)
					return nil, nil
				}
			})}}
			ref, err := parseProfile("urn:li:fsd_profile:ACoExamplePerson")
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.mutuals(context.Background(), ref, 10)
			if err == nil || len(result.People) != 0 {
				t.Fatalf("generic first-degree list was accepted as target-specific mutuals: %+v, %v", result, err)
			}
			if searches != 0 {
				t.Fatalf("unconfirmed target triggered %d guessed searches", searches)
			}
		})
	}
}
