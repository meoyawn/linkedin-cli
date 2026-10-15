# linkedin

A Go CLI that returns LinkedIn company people, profiles, mutual connections, and messages as JSON using your browser's
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
linkedin people profile --help
linkedin people mutuals --help
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

## Profiles and referral introductions

```sh
linkedin people profile example-person
linkedin people profile https://www.linkedin.com/in/example-person/
linkedin people mutuals example-person --limit 3
linkedin people mutuals urn:li:fsd_profile:ACoExample
```

`people profile` returns the name, headline, summary, canonical profile URL,
available location, and profile/member URNs in one API request. It does not
export private contact fields or fetch the full experience history.

`people mutuals` resolves the target profile, loads its document, and follows
only a link explicitly labelled as mutual connections by that profile, for its
exact identifier and first-degree network. If LinkedIn does not expose that
action, it fails without searching or returning generic contacts. Successful output includes
`verified: true`, the canonical target profile, the observed `search_url`, and
the source label in `profile_evidence`; older cached output without verification
is unverified. Verification establishes the LinkedIn introduction path, not
willingness to refer or the strength of the relationship.
It reads up to 10 contacts from the first page and reports `has_more` without
fetching another page. `--limit` accepts 1–10. All inputs need three sequential
requests on success, including profile URNs. It parses result cards using
semantic attributes, ignoring sidebar/related-person links and scripts.
An unrecognized response or non-first-degree result fails explicitly.
No scripts, analytics, messages, or connection requests are executed.
Results are potential introduction paths; willingness to refer remains unconfirmed.

To inspect a changed response, pass `--save-response private/mutuals.html`
after creating the ignored `private/` directory. The new capture file is written
with mode 0600 and is never overwritten. It can contain private profile data.

## Direct messages and free InMail

```sh
linkedin messages read example-person
linkedin messages send example-person --text-file /path/to/message.txt
linkedin messages send example-person --text-file /path/to/message.txt --send
```

`messages send` previews by default. Show the human the exact recipient and final
text, including links, and wait for explicit approval before adding `--send`.
Use UTF-8 plain text with ordinary URLs: LinkedIn does not interpret Markdown.
File contents, including newlines, are sent unchanged.

For a new conversation, preview and send both check the exact recipient's
compose options. Open Profile recipients with a confirmed "Free message"
context use the InMail payload automatically; preview returns `free_inmail:true`.
No subject is added. Paid, blocked, unknown, or unsupported compose modes stop
without sending or spending credits.

Both commands identify the signed-in mailbox and resolve the recipient, then
look up their direct conversation. If present, they verify its two participants
before reading one page of recent messages. Reading sends no read receipts.
No historical pagination is performed. Sending first checks that page for an
identical outgoing message and returns `already_present:true` without sending
it again. This does not search older messages or prevent concurrent sends.

An actual send makes a single POST using the observed `createMessage` contract.
Only a matching server receipt is reported as `sent:true`. No send is retried
automatically. After an uncertain outcome, read the conversation and inspect
the returned text and origin token before deciding whether to send again.
Test changes with local fixtures or mocks; never send live test messages.

## HTTP budget

Reading messages uses three GET requests without an existing conversation and
four with one. A new-message preview/send adds one compose-options GET and,
for free InMail, one compose-context GET. Actual sending adds one POST.

The people API accepts the company ID directly, so no general search or HTML
page load is needed.

- Slug or slug URL: one lookup, then one request per page.
- Numeric ID, ID URL, or organization URN: one request per page.
- Pages request up to 49 people, with smaller counts for the final limit.
- Requests within one command start at least two seconds apart. Run separate
  commands sequentially and wait at least two seconds between them; independent
  processes do not share a rate limiter. These are local operating defaults,
  not published LinkedIn safe limits.
- No browser navigation, profile-by-profile lookups, analytics, or eager
  pagination. No automatic retries on failures or rate limits.

Authentication redirects, checkpoints, and rate limits stop the command; it
does not attempt to work around them. Re-import cookies after signing in again.
A later page that unexpectedly reports zero total results fails explicitly
instead of claiming an incomplete export is complete. These limits reduce
unnecessary traffic; unofficial API access can still trigger LinkedIn checks.

This uses LinkedIn's browser API and observed persisted query IDs.
If LinkedIn changes that API, the CLI reports an error. HAR files, credentials,
and the compiled executable are excluded from version control. Test fixtures
retain the captured response structure with synthetic people and no cookies.

HTTP and transport failures include detailed diagnostics on stderr: method,
URL, timing, selected request/response headers, request JSON structure, and up
to 16 KiB of the error response (including service error codes and messages).
Cookie/auth headers are omitted; credentials, message text, and subjects are
redacted. stdout remains reserved for successful JSON results. Diagnostics can
still contain recipient identifiers and server error details, so review them
before sharing.

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
