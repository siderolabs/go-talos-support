// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

//go:build ignore

// Package main implements a generator which builds the default age recipients
// file from the public SSH keys of the public members of a GitHub organization.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	org        = "siderolabs"
	outputFile = "recipients.txt"
)

// githubUser is a subset of the GitHub user representation we care about.
type githubUser struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

// recipient holds the resolved data for a single organization member.
type recipient struct {
	login string
	name  string
	keys  []string
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	client := &http.Client{Timeout: 30 * time.Second}
	token := os.Getenv("GITHUB_TOKEN")

	logins, err := fetchPublicMembers(ctx, client, token, org)
	if err != nil {
		return fmt.Errorf("failed to list public members of %q: %w", org, err)
	}

	recipients := make([]recipient, 0, len(logins))

	for _, login := range logins {
		user, err := fetchUser(ctx, client, token, login)
		if err != nil {
			return fmt.Errorf("failed to fetch profile for %q: %w", login, err)
		}

		keys, err := fetchKeys(ctx, client, login)
		if err != nil {
			return fmt.Errorf("failed to fetch SSH keys for %q: %w", login, err)
		}

		if len(keys) == 0 {
			// skip members without any public SSH keys: they can't be recipients
			fmt.Fprintf(os.Stderr, "warning: %q has no public SSH keys, skipping\n", login)

			continue
		}

		recipients = append(recipients, recipient{
			login: user.Login,
			name:  user.Name,
			keys:  keys,
		})
	}

	// sort recipients by login for a stable, reviewable output
	sort.Slice(recipients, func(i, j int) bool {
		return strings.ToLower(recipients[i].login) < strings.ToLower(recipients[j].login)
	})

	return writeRecipients(outputFile, recipients)
}

// fetchPublicMembers returns the logins of all public members of the organization.
func fetchPublicMembers(ctx context.Context, client *http.Client, token, org string) ([]string, error) {
	var logins []string

	for page := 1; ; page++ {
		url := fmt.Sprintf("https://api.github.com/orgs/%s/public_members?per_page=100&page=%d", org, page)

		var users []githubUser

		if err := getJSON(ctx, client, token, url, &users); err != nil {
			return nil, err
		}

		if len(users) == 0 {
			break
		}

		for _, u := range users {
			logins = append(logins, u.Login)
		}
	}

	return logins, nil
}

// fetchUser returns the profile of a single GitHub user.
func fetchUser(ctx context.Context, client *http.Client, token, login string) (githubUser, error) {
	var user githubUser

	url := fmt.Sprintf("https://api.github.com/users/%s", login)

	if err := getJSON(ctx, client, token, url, &user); err != nil {
		return githubUser{}, err
	}

	return user, nil
}

// fetchKeys returns the public SSH keys for a user via https://github.com/<login>.keys.
func fetchKeys(ctx context.Context, client *http.Client, login string) ([]string, error) {
	url := fmt.Sprintf("https://github.com/%s.keys", login)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s: %s", resp.Status, bytes.TrimSpace(body))
	}

	var keys []string

	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		key := strings.TrimSpace(scanner.Text())
		if key == "" {
			continue
		}

		// age only supports ssh-rsa and ssh-ed25519 keys; other types
		// (ecdsa-*, sk-* security keys, ...) would make age fail to parse
		// the recipients file, so skip them here.
		if !strings.HasPrefix(key, "ssh-rsa ") && !strings.HasPrefix(key, "ssh-ed25519 ") {
			fmt.Fprintf(os.Stderr, "warning: %q has an unsupported SSH key type, skipping that key\n", login)

			continue
		}

		keys = append(keys, key)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// sort keys within a user for stable output
	sort.Strings(keys)

	return keys, nil
}

// getJSON performs an authenticated GET request and decodes the JSON response into v.
func getJSON(ctx context.Context, client *http.Client, token, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s: %s", resp.Status, bytes.TrimSpace(body))
	}

	return json.Unmarshal(body, v)
}

// writeRecipients writes the age recipients file, annotating each block of keys
// with a comment carrying the GitHub username and real name of the member.
func writeRecipients(path string, recipients []recipient) error {
	var buf bytes.Buffer

	buf.WriteString("# This file is generated by default_recipients.go, DO NOT EDIT.\n")
	fmt.Fprintf(&buf, "# It contains the public SSH keys of the public members of the %q GitHub organization.\n", org)
	buf.WriteString("#\n")
	buf.WriteString("# Regenerate with: go generate ./...\n")
	buf.WriteString("\n")

	for _, r := range recipients {
		if r.name != "" {
			fmt.Fprintf(&buf, "# %s (%s)\n", r.login, r.name)
		} else {
			fmt.Fprintf(&buf, "# %s\n", r.login)
		}

		for _, key := range r.keys {
			buf.WriteString(key + "\n")
		}

		buf.WriteString("\n")
	}

	return os.WriteFile(path, buf.Bytes(), 0o644)
}
