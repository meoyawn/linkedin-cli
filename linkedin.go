package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Observed persisted query IDs are shared API identifiers, not credentials.
// Batches of 49 work; 100 silently returns zero results for a nonempty company.
const (
	companyQueryID = "voyagerOrganizationDashCompanies.513e1fead204f45d2d951de15bb8461d"
	peopleQueryID  = "voyagerSearchDashClusters.e438ab99259203e9c1cd3f358e217282"
	pageSize       = 49
)

var (
	digits      = regexp.MustCompile(`^[0-9]+$`)
	companySlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

type companyRef struct {
	ID   string `json:"id"`
	Slug string `json:"slug,omitempty"`
	URL  string `json:"url"`
}

func parseCompany(value string) (companyRef, error) {
	original := value
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"urn:li:organization:", "urn:li:fsd_company:"} {
		if strings.HasPrefix(value, prefix) {
			value = strings.TrimPrefix(value, prefix)
			if !digits.MatchString(value) {
				return companyRef{}, fmt.Errorf("invalid company URN %q: expected a numeric ID", original)
			}
		}
	}
	if strings.HasPrefix(value, "www.linkedin.com/") || strings.HasPrefix(value, "linkedin.com/") {
		value = "https://" + value
	}
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || (u.Host != "www.linkedin.com" && u.Host != "linkedin.com") {
			return companyRef{}, fmt.Errorf("invalid company URL %q: expected https://www.linkedin.com/company/<slug-or-id>/", original)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 || parts[0] != "company" {
			return companyRef{}, fmt.Errorf("invalid company URL %q: expected /company/<slug-or-id>/", original)
		}
		value = parts[1]
	}
	value = strings.ToLower(value)
	company := companyRef{URL: "https://www.linkedin.com/company/" + value + "/"}
	if digits.MatchString(value) {
		if strings.TrimLeft(value, "0") == "" {
			return companyRef{}, fmt.Errorf("invalid company ID %q: expected a positive ID", original)
		}
		company.ID = value
	} else if companySlug.MatchString(value) {
		company.Slug = value
	} else {
		return companyRef{}, fmt.Errorf("invalid company %q: use its LinkedIn slug (for example example-company), numeric ID, or company URL", original)
	}
	return company, nil
}

type person struct {
	Name             string `json:"name"`
	Headline         string `json:"headline"`
	Location         string `json:"location"`
	URL              string `json:"url"`
	ProfileURN       string `json:"profile_urn,omitempty"`
	MemberURN        string `json:"member_urn,omitempty"`
	ConnectionDegree string `json:"connection_degree,omitempty"`
}

type peopleResult struct {
	Company   companyRef `json:"company"`
	People    []person   `json:"people"`
	Total     int        `json:"total"`
	Returned  int        `json:"returned"`
	Start     int        `json:"start"`
	HasMore   bool       `json:"has_more"`
	NextStart *int       `json:"next_start"`
}

type peopleOptions struct {
	limit int
	start int
	all   bool
}

type linkedinClient struct {
	http    *http.Client
	session session
}

type graphResponse struct {
	Data struct {
		Data struct {
			Companies *struct {
				Elements []string `json:"*elements"`
			} `json:"organizationDashCompaniesByUniversalName"`
			Search *searchCollection `json:"searchDashClustersByAll"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	} `json:"data"`
	Included []entityResult    `json:"included"`
	Errors   []json.RawMessage `json:"errors"`
}

type textView struct {
	Text string `json:"text"`
}

type entityResult struct {
	URN         string   `json:"entityUrn"`
	Title       textView `json:"title"`
	Headline    textView `json:"primarySubtitle"`
	Location    textView `json:"secondarySubtitle"`
	Degree      textView `json:"badgeText"`
	URL         string   `json:"navigationUrl"`
	TrackingURN string   `json:"trackingUrn"`
}

type searchItem struct {
	Item struct {
		EntityRef string         `json:"*entityResult"`
		Entity    *entityResult  `json:"entityResult"`
		Cluster   *searchCluster `json:"cluster"`
	} `json:"item"`
}

type searchCluster struct {
	Items []searchItem `json:"items"`
}

type searchCollection struct {
	Metadata struct {
		Total   *int            `json:"totalResultCount"`
		Blocked json.RawMessage `json:"blockedQueryError"`
	} `json:"metadata"`
	Paging *struct {
		Start int `json:"start"`
		Count int `json:"count"`
		Total int `json:"total"`
	} `json:"paging"`
	Elements []searchCluster `json:"elements"`
}

func (c *linkedinClient) query(ctx context.Context, queryID, variables string) (graphResponse, error) {
	// Rest.li's parentheses, commas, and colons must remain literal. Applying
	// url.Values.Encode to this expression makes LinkedIn reject it with HTTP 400.
	// All variable values are validated slugs or decimal IDs/offsets.
	query := "includeWebMetadata=true&variables=" + variables + "&queryId=" + queryID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.linkedin.com/voyager/api/graphql?"+query, nil)
	if err != nil {
		return graphResponse{}, fmt.Errorf("construct LinkedIn request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.linkedin.normalized+json+2.1")
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	req.Header.Set("X-Li-Lang", "en_US")
	req.Header.Set("Csrf-Token", c.session.JSessionID)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36")
	req.AddCookie(&http.Cookie{Name: "li_at", Value: c.session.LIAT})
	req.AddCookie(&http.Cookie{Name: "JSESSIONID", Value: c.session.JSessionID, Quoted: true})
	resp, err := c.http.Do(req)
	if err != nil {
		return graphResponse{}, fmt.Errorf("request LinkedIn: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, 999:
		return graphResponse{}, fmt.Errorf("LinkedIn returned HTTP %d: sign in in your browser, complete any checkpoint, and run linkedin auth login again", resp.StatusCode)
	case http.StatusTooManyRequests:
		return graphResponse{}, fmt.Errorf("LinkedIn rate limit (HTTP 429); try later (Retry-After: %q)", resp.Header.Get("Retry-After"))
	default:
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return graphResponse{}, fmt.Errorf("LinkedIn redirected the request (HTTP %d); sign in in your browser and run linkedin auth login again", resp.StatusCode)
		}
		return graphResponse{}, fmt.Errorf("LinkedIn returned HTTP %d", resp.StatusCode)
	}
	var result graphResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&result); err != nil {
		return graphResponse{}, fmt.Errorf("LinkedIn did not return valid API JSON; check your browser session and run linkedin auth login again")
	}
	if len(result.Errors) > 0 || len(result.Data.Errors) > 0 {
		return graphResponse{}, fmt.Errorf("LinkedIn rejected persisted query %s; its API may have changed", queryID)
	}
	return result, nil
}

func (c *linkedinClient) resolve(ctx context.Context, company companyRef) (companyRef, error) {
	if company.ID != "" {
		return company, nil
	}
	result, err := c.query(ctx, companyQueryID, "(universalName:"+company.Slug+")")
	if err != nil {
		return companyRef{}, err
	}
	if result.Data.Data.Companies == nil {
		return companyRef{}, fmt.Errorf("LinkedIn company lookup response has changed (missing organizationDashCompaniesByUniversalName)")
	}
	for _, urn := range result.Data.Data.Companies.Elements {
		id, ok := strings.CutPrefix(urn, "urn:li:fsd_company:")
		if ok && digits.MatchString(id) {
			company.ID = id
			return company, nil
		}
	}
	return companyRef{}, fmt.Errorf("company slug %q was not found; use a LinkedIn company URL or numeric ID", company.Slug)
}

func (c *linkedinClient) people(ctx context.Context, company companyRef, opts peopleOptions) (peopleResult, error) {
	company, err := c.resolve(ctx, company)
	if err != nil {
		return peopleResult{}, err
	}
	result := peopleResult{Company: company, People: []person{}, Start: opts.start}
	seen := make(map[string]bool)
	start := opts.start
	for {
		count := pageSize
		if !opts.all {
			count = min(count, opts.limit-len(result.People))
		}
		variables := fmt.Sprintf("(start:%d,origin:FACETED_SEARCH,query:(flagshipSearchIntent:ORGANIZATIONS_PEOPLE_ALUMNI,queryParameters:List((key:currentCompany,value:List(%s)),(key:resultType,value:List(ORGANIZATION_ALUMNI))),includeFiltersInResponse:false),count:%d)", start, company.ID, count)
		response, err := c.query(ctx, peopleQueryID, variables)
		if err != nil {
			return peopleResult{}, fmt.Errorf("fetch people at offset %d: %w", start, err)
		}
		page := response.Data.Data.Search
		if page == nil || page.Paging == nil {
			return peopleResult{}, fmt.Errorf("LinkedIn people response has changed (missing searchDashClustersByAll or paging)")
		}
		if len(page.Metadata.Blocked) > 0 && string(page.Metadata.Blocked) != "null" {
			return peopleResult{}, fmt.Errorf("LinkedIn blocked the people search; check the search in your browser")
		}
		if page.Paging.Start != start || page.Paging.Count <= 0 || page.Paging.Count > count || page.Paging.Total < 0 {
			return peopleResult{}, fmt.Errorf("LinkedIn returned unexpected paging at offset %d", start)
		}
		people, err := page.people(response.Included)
		if err != nil {
			return peopleResult{}, err
		}
		result.Total = page.Paging.Total
		if page.Metadata.Total != nil {
			result.Total = *page.Metadata.Total
		}
		previousCount := len(result.People)
		for _, p := range people {
			key := p.ProfileURN
			if key == "" {
				key = p.URL
			}
			if key == "" || !seen[key] {
				seen[key] = true
				result.People = append(result.People, p)
			}
		}
		next := start + page.Paging.Count
		if next <= start {
			return peopleResult{}, fmt.Errorf("LinkedIn pagination did not advance from offset %d", start)
		}
		result.HasMore = next < result.Total && len(people) > 0
		if len(people) == 0 && start < result.Total {
			return peopleResult{}, fmt.Errorf("LinkedIn returned no people at offset %d despite reporting %d results; search may be restricted", start, result.Total)
		}
		if result.HasMore {
			result.NextStart = &next
		} else {
			result.NextStart = nil
		}
		if !result.HasMore || (!opts.all && len(result.People) >= opts.limit) {
			break
		}
		if len(result.People) == previousCount {
			return peopleResult{}, fmt.Errorf("LinkedIn repeated a page at offset %d; stopped to avoid unnecessary requests", start)
		}
		start = next
	}
	result.Returned = len(result.People)
	return result, nil
}

func (s searchCollection) people(included []entityResult) ([]person, error) {
	byURN := make(map[string]entityResult, len(included))
	for _, e := range included {
		byURN[e.URN] = e
	}
	var people []person
	var visit func(searchCluster) error
	visit = func(cluster searchCluster) error {
		for _, item := range cluster.Items {
			if item.Item.Cluster != nil {
				if err := visit(*item.Item.Cluster); err != nil {
					return err
				}
			}
			e := item.Item.Entity
			if item.Item.EntityRef != "" {
				resolved, ok := byURN[item.Item.EntityRef]
				if !ok {
					return fmt.Errorf("LinkedIn people response references a missing result; API response may have changed")
				}
				e = &resolved
			}
			if e == nil {
				continue
			}
			profileURN := ""
			if _, suffix, ok := strings.Cut(e.URN, "(urn:li:fsd_profile:"); ok {
				id, _, _ := strings.Cut(suffix, ",")
				profileURN = "urn:li:fsd_profile:" + id
			}
			profileURL := e.URL
			if u, err := url.Parse(profileURL); err == nil {
				u.RawQuery, u.Fragment = "", ""
				profileURL = u.String()
			}
			people = append(people, person{
				Name: e.Title.Text, Headline: e.Headline.Text, Location: e.Location.Text,
				URL: profileURL, ProfileURN: profileURN, MemberURN: e.TrackingURN,
				ConnectionDegree: strings.TrimSpace(strings.TrimPrefix(e.Degree.Text, "•")),
			})
		}
		return nil
	}
	for _, cluster := range s.Elements {
		if err := visit(cluster); err != nil {
			return nil, err
		}
	}
	return people, nil
}
