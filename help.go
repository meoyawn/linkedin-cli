package main

import (
	"fmt"
	"io"
	"strings"
)

const rootHelp = `LinkedIn company people, as JSON.

Usage:
  linkedin <command> [flags]

Commands:
  companies people <company>  List people at a company
  auth login                  Import browser cookies with SweetCookie
  auth status                 Check saved cookies locally
  auth logout                 Remove saved cookies
  help [command]              Show help for a command

Quick start:
  linkedin auth login --browser chrome
  linkedin companies people example-company
  linkedin companies people example-company --all

Options:
  -h, --help                  Show help

People:
  Accepts a company slug, numeric ID, LinkedIn company URL, or organization URN.
  Returns up to %d people by default. Use --all to fetch every available page.
  Results follow LinkedIn's search order. Numeric IDs skip the company lookup.

Output:
  Commands print JSON to stdout; errors go to stderr and exit nonzero.

Environment:
  LINKEDIN_CONFIG_DIR         Override the local cookie storage directory

Run 'linkedin <command> --help' for command options and examples.
`

const companiesHelp = `Look up people at LinkedIn companies.

Usage:
  linkedin companies <command>

Commands:
  people <company>           List people at a company as JSON

Examples:
  linkedin companies people example-company
  linkedin companies people https://www.linkedin.com/company/example-company/

Run 'linkedin companies people --help' for options.
`

const peopleHelp = `List people at a LinkedIn company as JSON.

Usage:
  linkedin companies people <company> [flags]

Arguments:
  <company>                  Company slug, numeric ID, LinkedIn company URL,
                             or organization URN

Options:
      --limit N              Maximum people to return (default: %d)
      --start N              Start at this result offset (default: 0)
      --all                  Fetch every available page; overrides --limit
  -h, --help                 Show help

Examples:
  linkedin companies people example-company
  linkedin companies people example-company --all
  linkedin companies people example-company --limit 10
  linkedin companies people 123456789

Flags may precede or follow the company. Sign in with 'linkedin auth login'.
Each people request fetches up to %d results. Slugs need one extra lookup;
numeric IDs skip it. Requests are not retried automatically.

Output includes company, people, total, returned, start, has_more, and next_start.
Use next_start with --start to continue. Errors exit nonzero without partial JSON.
`

const authHelp = `Manage the browser cookies used to sign in to LinkedIn.

Usage:
  linkedin auth <command> [flags]

Commands:
  login                      Import cookies with SweetCookie
  status                     Check saved cookies locally
  logout                     Remove the local cookie copy

Examples:
  linkedin auth login --browser chrome
  linkedin auth login --browser arc
  linkedin auth status

Cookies are stored locally with restricted file permissions. Auth commands
make no HTTP requests. Set LINKEDIN_CONFIG_DIR to choose the storage directory.

Run 'linkedin auth <command> --help' for options.
`

const loginHelp = `Import your signed-in LinkedIn browser session with SweetCookie.

Usage:
  linkedin auth login [flags]

Options:
      --browser NAME         Browser to read (default: auto, first usable session)
      --profile NAME         Profile name, directory, or cookie database;
                             requires an explicit --browser
      --cookies-file PATH    Import cookie JSON instead of reading a browser;
                             cannot be combined with --browser or --profile
  -h, --help                 Show help

Examples:
  linkedin auth login
  linkedin auth login --browser chrome --profile 'Your Chrome'
  linkedin auth login --browser arc
  linkedin auth login --cookies-file /path/to/cookies.json

Browsers:
  chrome, chromium, edge, brave, vivaldi, opera, arc, helium, dia, comet, atlas,
  whale, firefox, zen, floorp, waterfox, librewolf, safari
  Availability depends on your operating system.

Sign in at linkedin.com in your browser first. Import may prompt for Keychain
or keyring access. Cookie JSON accepts an array or an object with a cookies array.
Both li_at and JSESSIONID must belong to the same browser profile.

Only those two cookies are saved. Output contains status, never cookie values.
`

const statusHelp = `Check whether saved LinkedIn cookies exist and have expired.

Usage:
  linkedin auth status

Options:
  -h, --help                 Show help

Prints JSON with authenticated, source, and available expiry information.
This is a local check; it does not verify the session with LinkedIn.
`

const logoutHelp = `Remove the local copy of your LinkedIn cookies.

Usage:
  linkedin auth logout

Options:
  -h, --help                 Show help

Prints JSON confirming removal. Your browser remains signed in.
`

func isHelpFlag(arg string) bool { return arg == "--help" || arg == "-h" }

func writeHelp(w io.Writer, commands []string) error {
	var help string
	switch topic := strings.Join(commands, " "); topic {
	case "":
		help = fmt.Sprintf(rootHelp, pageSize)
	case "companies":
		help = companiesHelp
	case "companies people":
		help = fmt.Sprintf(peopleHelp, pageSize, pageSize)
	case "auth":
		help = authHelp
	case "auth login":
		help = loginHelp
	case "auth status":
		help = statusHelp
	case "auth logout":
		help = logoutHelp
	default:
		return fmt.Errorf("unknown help topic %q; run linkedin --help", topic)
	}
	_, err := io.WriteString(w, help)
	return err
}
