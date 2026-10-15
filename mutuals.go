package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

const mutualsPageSize = 10

type mutualsResult struct {
	Profile         profileRef `json:"profile"`
	SearchURL       string     `json:"search_url"`
	ProfileEvidence string     `json:"profile_evidence"`
	People          []person   `json:"people"`
	Returned        int        `json:"returned"`
	HasMore         bool       `json:"has_more"`
	Verified        bool       `json:"verified"`
}

func (c *linkedinClient) mutuals(ctx context.Context, ref profileRef, limit int) (mutualsResult, error) {
	if limit < 1 || limit > mutualsPageSize {
		return mutualsResult{}, fmt.Errorf("mutuals --limit must be between 1 and %d; got %d", mutualsPageSize, limit)
	}
	profile, err := c.profile(ctx, ref)
	if err != nil {
		return mutualsResult{}, err
	}
	id := strings.TrimPrefix(profile.ProfileURN, "urn:li:fsd_profile:")
	if ref.ID != "" && ref.ID != id {
		return mutualsResult{}, fmt.Errorf("LinkedIn resolved a different target profile; mutual connections are unconfirmed")
	}
	ref, err = parseProfile(profile.URL)
	if err != nil {
		return mutualsResult{}, fmt.Errorf("resolve mutual-connections target: %w", err)
	}
	ref.ID = id
	profileURL, _ := url.Parse(ref.URL)
	profileResponse, err := c.get(ctx, profileURL.RequestURI(), http.Header{"Accept": {"text/html"}, "Accept-Language": {"en-US,en;q=0.9"}})
	if err != nil {
		return mutualsResult{}, fmt.Errorf("fetch mutual-connections target: %w", err)
	}
	path, evidence, err := mutualsPath(io.LimitReader(profileResponse.Body, 8<<20), ref.ID)
	profileResponse.Body.Close()
	if err != nil {
		return mutualsResult{}, err
	}
	resp, err := c.get(ctx, path, http.Header{"Accept": {"text/html"}, "Accept-Language": {"en-US,en;q=0.9"}})
	if err != nil {
		return mutualsResult{}, fmt.Errorf("fetch mutual connections: %w", err)
	}
	defer resp.Body.Close()
	var body io.Reader = io.LimitReader(resp.Body, 8<<20)
	if c.mutualsResponse != nil {
		body = io.TeeReader(body, c.mutualsResponse)
	}
	people, hasMore, err := parseMutualsPage(body)
	if err != nil {
		return mutualsResult{}, err
	}
	if len(people) > limit {
		people, hasMore = people[:limit], true
	}
	return mutualsResult{Profile: ref, SearchURL: "https://www.linkedin.com" + path, ProfileEvidence: evidence, People: people, Returned: len(people), HasMore: hasMore, Verified: true}, nil
}

// LinkedIn can return generic contacts for a connectionOf request. Only follow
// a labelled mutual-search action exposed by the resolved target's profile;
// a fabricated URL or echoed query is not evidence.
func mutualsPath(r io.Reader, targetID string) (string, string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", "", fmt.Errorf("parse mutual-connections target: %w", err)
	}
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.Data != "a" {
			continue
		}
		u, err := url.Parse(attr(n, "href"))
		if err != nil || u.User != nil || u.Fragment != "" ||
			(u.Scheme != "" && u.Scheme != "https") ||
			(u.Host != "" && u.Host != "www.linkedin.com" && u.Host != "linkedin.com") ||
			u.Path != "/search/results/people/" {
			continue
		}
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(q["network"]) != 1 || len(q["connectionOf"]) != 1 {
			continue
		}
		var network, connections []string
		if json.Unmarshal([]byte(q.Get("network")), &network) != nil || len(network) != 1 || network[0] != "F" ||
			json.Unmarshal([]byte(q.Get("connectionOf")), &connections) != nil || len(connections) != 1 || connections[0] != targetID {
			continue
		}
		evidence := nodeText(n)
		if !strings.Contains(strings.ToLower(evidence), "mutual connection") {
			continue
		}
		return u.RequestURI(), evidence, nil
	}
	return "", "", fmt.Errorf("LinkedIn did not expose a labelled mutual-connections search for this target; the connection path is unconfirmed (unavailable or changed profile response); no generic connections returned")
}

func parseMutualsPage(r io.Reader) ([]person, bool, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, false, fmt.Errorf("parse mutual connections document: %w", err)
	}
	var screen *html.Node
	for n := range doc.Descendants() {
		if attr(n, "data-sdui-screen") == "com.linkedin.sdui.flagshipnav.search.SearchResultsPeople" {
			screen = n
			break
		}
	}
	if screen == nil {
		return nil, false, fmt.Errorf("LinkedIn did not return the mutual-connections search screen; check for a sign-in/checkpoint or changed response before retrying")
	}
	people := []person{}
	seen := make(map[string]bool)
	hasMore := false
	for n := range screen.Descendants() {
		if attr(n, "data-testid") == "pagination-controls-next-button-visible" {
			hasMore = true
		}
		if attr(n, "role") != "listitem" {
			continue
		}
		var p person
		var paragraphs []string
		for child := range n.Descendants() {
			if child.Type != html.ElementNode {
				continue
			}
			if child.Data == "p" {
				related := false
				if len(paragraphs) > 0 {
					for link := range child.Descendants() {
						if link.Data == "a" {
							href := attr(link, "href")
							if _, err := parseProfile(href); err == nil || strings.Contains(href, "/search/results/people/") {
								related = true
							}
						}
					}
				}
				if !related {
					paragraphs = append(paragraphs, nodeText(child))
				}
			}
			if child.Data == "a" && p.URL == "" {
				if ref, err := parseProfile(attr(child, "href")); err == nil {
					p.URL = ref.URL
				}
			}
			if id, ok := strings.CutPrefix(attr(child, "id"), "SearchResults"); ok && profileID.MatchString(id) {
				p.ProfileURN = "urn:li:fsd_profile:" + id
			}
		}
		if p.URL == "" || len(paragraphs) == 0 {
			return nil, false, fmt.Errorf("LinkedIn mutual-connections result structure has changed (profile link: %t, paragraphs: %d); no partial result returned", p.URL != "", len(paragraphs))
		}
		name, degree, ok := strings.Cut(paragraphs[0], "•")
		if !ok || strings.TrimSpace(degree) != "1st" {
			return nil, false, fmt.Errorf("LinkedIn mutual-connections search did not return first-degree contacts; stopped without partial results")
		}
		p.Name, p.ConnectionDegree = strings.TrimSpace(name), "1st"
		if len(paragraphs) > 1 {
			p.Headline = paragraphs[1]
		}
		if len(paragraphs) > 2 {
			p.Location = paragraphs[2]
		}
		if p.Name == "" {
			return nil, false, fmt.Errorf("LinkedIn mutual-connections result is missing a name")
		}
		if !seen[p.URL] {
			seen[p.URL] = true
			people = append(people, p)
		}
	}
	if len(people) == 0 {
		return nil, false, fmt.Errorf("LinkedIn returned no recognizable mutual contacts; an empty, restricted, or changed search cannot yet be distinguished")
	}
	return people, hasMore, nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	for child := range n.Descendants() {
		if child.Type == html.TextNode {
			b.WriteString(child.Data)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
