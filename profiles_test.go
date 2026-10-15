package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProfileSelectsReferencedEntityAndOmitsContactData(t *testing.T) {
	client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/voyager/api/identity/dash/profiles" || r.URL.Query().Get("q") != "memberIdentity" || r.URL.Query().Get("memberIdentity") != "example-person" {
			t.Fatalf("unexpected profile request: %s", r.URL)
		}
		return jsonResponse(fixture(t, "profile")), nil
	})}}
	ref, err := parseProfile("https://www.linkedin.com/in/example-person/?tracking=ignored")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.profile(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "Example Person" || result.Headline != "Backend engineer" || result.Summary != "Builds payment services." || result.Location != "Test City" || result.ProfileURN != "urn:li:fsd_profile:ACoExamplePerson" || result.URL != "https://www.linkedin.com/in/example-person/" {
		t.Fatalf("incorrect referenced profile: %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "not-exported") {
		t.Fatal("profile exported an unrelated contact field")
	}
}

func TestProfileUnavailableAndChangedResponsesFail(t *testing.T) {
	for _, body := range []string{
		`{"data":{"*elements":[]},"included":[]}`,
		`{"data":{"*elements":["urn:li:fsd_profile:ACoMissing"]},"included":[]}`,
		`{"data":{"*elements":["urn:li:fsd_profile:bad,id"]},"included":[{"entityUrn":"urn:li:fsd_profile:bad,id"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return jsonResponse([]byte(body)), nil
			})}}
			ref, err := parseProfile("example-person")
			if err != nil {
				t.Fatal(err)
			}
			if result, err := client.profile(context.Background(), ref); err == nil || result.Name != "" {
				t.Fatalf("unavailable profile returned success: %+v, %v", result, err)
			}
		})
	}
}
