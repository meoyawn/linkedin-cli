package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func jsonResponse(body []byte) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}
}

// The synthetic fixture's included array is not in search order. This also owns
// the request-count contract: one slug lookup plus one request per result page.
func TestPeoplePreservesSearchOrderAndPaginates(t *testing.T) {
	first := fixture(t, "people")
	var second graphResponse
	if err := json.Unmarshal(first, &second); err != nil {
		t.Fatal(err)
	}
	page := second.Data.Data.Search
	page.Paging.Start, page.Paging.Count = 12, 3
	page.Elements[0].Items = page.Elements[0].Items[:3]
	for i := range page.Elements[0].Items {
		oldURN := page.Elements[0].Items[i].Item.EntityRef
		newURN := fmt.Sprintf("urn:li:fsd_entityResultViewModel:(urn:li:fsd_profile:fake%02d,SEARCH_SRP,DEFAULT)", i+13)
		page.Elements[0].Items[i].Item.EntityRef = newURN
		for j := range second.Included {
			e := &second.Included[j]
			if e.URN == oldURN {
				e.URN = newURN
				e.Title.Text = fmt.Sprintf("Person %02d", i+13)
				e.URL = fmt.Sprintf("https://www.linkedin.com/in/person-%02d", i+13)
			}
		}
	}
	secondBody, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := linkedinClient{session: session{LIAT: "test-token", JSessionID: "ajax:test"}}
	client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.Host != "www.linkedin.com" || r.URL.Path != "/voyager/api/graphql" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Csrf-Token") != "ajax:test" || r.Header.Get("X-Restli-Protocol-Version") != "2.0.0" {
			t.Fatal("missing CSRF or Rest.li headers")
		}
		if cookie, err := r.Cookie("li_at"); err != nil || cookie.Value != "test-token" {
			t.Fatal("missing authentication cookie")
		}
		variables := r.URL.Query().Get("variables")
		if strings.Contains(r.URL.RawQuery, "%28") || strings.Contains(r.URL.RawQuery, "%3A") {
			t.Fatal("Rest.li syntax was percent-encoded; LinkedIn rejects this with HTTP 400")
		}
		switch calls {
		case 1:
			if variables != "(universalName:example-company)" {
				t.Fatalf("company lookup variables = %q", variables)
			}
			return jsonResponse(fixture(t, "company")), nil
		case 2:
			if !strings.Contains(variables, "start:0,") || !strings.Contains(variables, "currentCompany,value:List(123456789)") || !strings.Contains(variables, "count:15)") {
				t.Fatalf("first page variables = %q", variables)
			}
			return jsonResponse(first), nil
		case 3:
			if !strings.Contains(variables, "start:12,") || !strings.Contains(variables, "count:3)") {
				t.Fatalf("second page variables = %q", variables)
			}
			return jsonResponse(secondBody), nil
		default:
			t.Fatalf("unnecessary HTTP request %d", calls)
			return nil, fmt.Errorf("unexpected request")
		}
	})}
	company, err := parseCompany("https://www.linkedin.com/company/example-company/people/")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.people(context.Background(), company, peopleOptions{limit: 15})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || result.Returned != 15 || result.Total != 15 || result.HasMore || result.NextStart != nil {
		t.Fatalf("calls=%d result=%+v", calls, result)
	}
	for i, p := range result.People {
		if p.Name != fmt.Sprintf("Person %02d", i+1) || p.URL != fmt.Sprintf("https://www.linkedin.com/in/person-%02d", i+1) {
			t.Fatalf("person %d is out of order or has tracking parameters: %+v", i, p)
		}
	}
	if p := result.People[0]; p.Headline != "Engineer 01" || p.Location != "Test City" || p.ProfileURN != "urn:li:fsd_profile:fake01" || p.ConnectionDegree != "2nd" {
		t.Fatalf("incorrect person fields: %+v", p)
	}
}

func TestNumericCompanyUsesOneRequestAndReportsContinuation(t *testing.T) {
	calls := 0
	client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.Contains(r.URL.Query().Get("variables"), "currentCompany,value:List(123456789)") {
			t.Fatal("numeric company ID triggered a lookup")
		}
		return jsonResponse(fixture(t, "people")), nil
	})}}
	company, err := parseCompany("123456789")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.people(context.Background(), company, peopleOptions{limit: 12})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.Returned != 12 || !result.HasMore || result.NextStart == nil || *result.NextStart != 12 {
		t.Fatalf("calls=%d result=%+v", calls, result)
	}
}

func TestPeopleFailsWithoutRetriesOrPartialSuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"expired", 401, "", "auth login"},
		{"checkpoint", 403, "", "checkpoint"},
		{"limited", 429, "", "rate limit"},
		{"HTML", 200, "<html>login</html>", "valid API JSON"},
		{"GraphQL error", 200, `{"data":{"errors":[{"message":"error"}]}}`, "persisted query"},
		{"changed schema", 200, `{"data":{"data":{}}}`, "response has changed"},
		{"missing reference", 200, `{"data":{"data":{"searchDashClustersByAll":{"paging":{"start":0,"count":12,"total":1},"elements":[{"items":[{"item":{"*entityResult":"missing"}}]}]}}}}`, "missing result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			company, err := parseCompany("123456789")
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.people(context.Background(), company, peopleOptions{limit: 12})
			if err == nil || !strings.Contains(err.Error(), tc.want) || calls != 1 || len(result.People) != 0 {
				t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
			}
		})
	}
}

// LinkedIn can stop an export by reporting zero results on a later page.
// That must not turn a restricted search into a successful, complete export.
func TestPeopleRejectsDisappearingTotals(t *testing.T) {
	calls := 0
	client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResponse(fixture(t, "people")), nil
		}
		return jsonResponse([]byte(`{"data":{"data":{"searchDashClustersByAll":{"metadata":{"totalResultCount":0},"paging":{"start":12,"count":49,"total":0},"elements":[]}}},"included":[]}`)), nil
	})}}
	company, err := parseCompany("123456789")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.people(context.Background(), company, peopleOptions{all: true})
	if err == nil || !strings.Contains(err.Error(), "restricted") || calls != 2 || len(result.People) != 0 {
		t.Fatalf("restricted search reported success or retried: calls=%d result=%+v err=%v", calls, result, err)
	}
}
