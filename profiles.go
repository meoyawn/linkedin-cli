package main

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	profileIdentity = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_-]*$`)
	profileID       = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	profileLocale   = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]{2})?$`)
)

type profileRef struct {
	ID   string `json:"id,omitempty"`
	Slug string `json:"slug,omitempty"`
	URL  string `json:"url"`
}

func parseProfile(value string) (profileRef, error) {
	original := value
	value = strings.TrimSpace(value)
	if id, ok := strings.CutPrefix(value, "urn:li:fsd_profile:"); ok {
		if !profileID.MatchString(id) {
			return profileRef{}, fmt.Errorf("invalid profile URN %q: expected a profile identifier", original)
		}
		return profileRef{ID: id, URL: "https://www.linkedin.com/in/" + id + "/"}, nil
	}
	if strings.HasPrefix(value, "www.linkedin.com/") || strings.HasPrefix(value, "linkedin.com/") {
		value = "https://" + value
	}
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || (u.Host != "www.linkedin.com" && u.Host != "linkedin.com") {
			return profileRef{}, fmt.Errorf("invalid profile URL %q: expected https://www.linkedin.com/in/<person>/", original)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if (len(parts) != 2 && (len(parts) != 3 || !profileLocale.MatchString(parts[2]))) || parts[0] != "in" {
			return profileRef{}, fmt.Errorf("invalid profile URL %q: expected /in/<person>/", original)
		}
		value = parts[1]
	}
	if !profileIdentity.MatchString(value) {
		return profileRef{}, fmt.Errorf("invalid profile %q: use a LinkedIn profile slug, /in/ URL, or urn:li:fsd_profile identifier", original)
	}
	return profileRef{Slug: value, URL: "https://www.linkedin.com/in/" + url.PathEscape(value) + "/"}, nil
}

type profileResult struct {
	person
	Summary string `json:"summary"`
}

type profileEntity struct {
	URN              string `json:"entityUrn"`
	MemberURN        string `json:"objectUrn"`
	PublicIdentifier string `json:"publicIdentifier"`
	FirstName        string `json:"firstName"`
	LastName         string `json:"lastName"`
	Headline         string `json:"headline"`
	Summary          string `json:"summary"`
	Location         string `json:"locationName"`
}

func (c *linkedinClient) profile(ctx context.Context, ref profileRef) (profileResult, error) {
	identity := ref.Slug
	if ref.ID != "" {
		identity = ref.ID
	}
	var response struct {
		Data struct {
			Elements []string `json:"*elements"`
		} `json:"data"`
		Included []profileEntity `json:"included"`
	}
	if err := c.getJSON(ctx, "/voyager/api/identity/dash/profiles?q=memberIdentity&memberIdentity="+url.QueryEscape(identity), &response); err != nil {
		return profileResult{}, fmt.Errorf("fetch profile %q: %w", identity, err)
	}
	if len(response.Data.Elements) == 0 {
		return profileResult{}, fmt.Errorf("profile %q was not found or is unavailable to your account", identity)
	}
	for _, p := range response.Included {
		if p.URN != response.Data.Elements[0] {
			continue
		}
		id, ok := strings.CutPrefix(p.URN, "urn:li:fsd_profile:")
		if !ok || !profileID.MatchString(id) {
			return profileResult{}, fmt.Errorf("LinkedIn profile response has changed (invalid profile identifier)")
		}
		profileURL := ref.URL
		if p.PublicIdentifier != "" {
			profileURL = "https://www.linkedin.com/in/" + url.PathEscape(p.PublicIdentifier) + "/"
		}
		return profileResult{person: person{
			Name: strings.TrimSpace(p.FirstName + " " + p.LastName), Headline: p.Headline,
			Location: p.Location, URL: profileURL, ProfileURN: p.URN, MemberURN: p.MemberURN,
		}, Summary: p.Summary}, nil
	}
	return profileResult{}, fmt.Errorf("LinkedIn profile response references a missing profile; API response may have changed")
}
