# linkedin

A Go CLI that returns LinkedIn company people as JSON using your browser's
signed-in session. Cookie import uses [SweetCookie](https://github.com/steipete/sweetcookie/).

```sh
go build -o linkedin .
./linkedin auth login --browser chrome
./linkedin companies people example-company
```

To install `linkedin` on your PATH:

```sh
go install .
```

Requires Go 1.25 or newer. Examples use placeholder company slugs and IDs;
replace them with the company you want to look up.

```sh
linkedin --help
linkedin companies people --help
linkedin auth login --help
```

Every command supports `-h` and `--help`. Use `linkedin help <command>` for
the same help, for example `linkedin help companies people`.

## Authentication

Sign in to linkedin.com in your browser, then import its cookies:

```sh
linkedin auth login                         # first usable browser session
linkedin auth login --browser chrome --profile 'Your Chrome'
linkedin auth login --browser arc
linkedin auth login --cookies-file /path/to/cookies.json
linkedin auth status
linkedin auth logout
```

The JSON import accepts SweetCookie's cookie array or `{ "cookies": [...] }`
format. It must contain `li_at` and `JSESSIONID` for `.linkedin.com` from the
same signed-in browser profile. Browser import may prompt for Keychain/keyring
access. Only those two cookies are saved, in `cookies.json` under the OS user
configuration directory's `linkedin` folder (on macOS,
`~/Library/Application Support/linkedin`). The directory is mode `0700`, and
the file is mode `0600`. Set `LINKEDIN_CONFIG_DIR` to choose another directory.

Auth commands work locally without HTTP calls. `auth status` checks whether the
saved cookies exist and have expired; it does not validate them with LinkedIn.
Cookie values are never included in command output. `logout` removes the local
copy; it does not sign your browser out. Re-run `auth login` after refreshing
your browser session if LinkedIn rejects the cookies.

## Company people

Company accepts the URL slug, numeric ID,
company URL (including `/people/`), or organization URN:

```sh
linkedin companies people example-company
linkedin companies people https://www.linkedin.com/company/example-company/
linkedin companies people 123456789
linkedin companies people example-company --limit 24
linkedin companies people example-company --start 49
linkedin companies people example-company --all
```

The default returns up to 49 people. `--limit N` requests up to N;
`--all` fetches all results LinkedIn makes available and overrides `--limit`.
`--start N` resumes at a result offset. Flags can precede or follow the company.

Output includes `company`, `people`, `total`, `returned`, `start`, `has_more`,
and `next_start`. Each person has `name`, `headline`, `location`, `url`, and
available profile/member URNs and connection degree. Results retain LinkedIn's
search order; profile URLs have tracking parameters removed. `next_start` is
`null` when there are no more pages. `total` is LinkedIn's reported search count,
which can change or be restricted by your account.

Successful commands emit JSON to stdout. Help is plain text. Diagnostics go to
stderr; failures exit nonzero without emitting a partial result.

## HTTP budget

The people API accepts the company ID directly, so no general search or HTML
page load is needed.

- Slug or slug URL: one lookup, then one request per page.
- Numeric ID, ID URL, or organization URN: one request per page.
- Pages request up to 49 people, with smaller counts for the final limit.
- No browser navigation, profile-by-profile lookups, analytics, or eager
  pagination. No automatic retries on failures or rate limits.

This uses LinkedIn's browser API and observed persisted query IDs.
If LinkedIn changes that API, the CLI reports an error. HAR files, credentials,
and the compiled executable are excluded from version control. Test fixtures
retain the captured response structure with synthetic people and no cookies.

```sh
go test ./...
go vet ./...
```

## Privacy

Browser cookies and exported people data are private. Keep them outside this
repository, and remove cookies, authorization headers, profile identifiers,
and personal data before sharing logs, network captures, or bug reports.
The fixtures in `testdata/` use synthetic people and placeholder company IDs.

## License

Copyright (C) 2026 meoyawn. Licensed under the GNU General Public License,
version 3 only ([GPL-3.0-only](LICENSE)).
