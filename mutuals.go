package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

const mutualsPageSize = 10

type mutualsResult struct {
	Profile   profileRef `json:"profile"`
	SearchURL string     `json:"search_url"`
	People    []person   `json:"people"`
	Returned  int        `json:"returned"`
	HasMore   bool       `json:"has_more"`
}

func (c *linkedinClient) mutuals(ctx context.Context, ref profileRef, limit int) (mutualsResult, error) {
	if limit < 1 || limit > mutualsPageSize {
		return mutualsResult{}, fmt.Errorf("mutuals --limit must be between 1 and %d; got %d", mutualsPageSize, limit)
	}
	if ref.ID == "" {
		profile, err := c.profile(ctx, ref)
		if err != nil {
			return mutualsResult{}, err
		}
		ref.ID = strings.TrimPrefix(profile.ProfileURN, "urn:li:fsd_profile:")
		ref.URL = profile.URL
	}
	// Captured from the signed-in mutual-connections view. The server returns
	// the first page in the document; no scripts or RSC side effects are needed.
	query := url.Values{
		"origin":       {"MEMBER_PROFILE_CANNED_SEARCH"},
		"network":      {`["F"]`},
		"connectionOf": {`["` + ref.ID + `"]`},
	}
	path := "/search/results/people/?" + query.Encode()
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
	return mutualsResult{Profile: ref, SearchURL: "https://www.linkedin.com" + path, People: people, Returned: len(people), HasMore: hasMore}, nil
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
