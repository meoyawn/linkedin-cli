package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
)

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		return writeHelp(stdout, nil)
	}
	if args[0] == "help" {
		return writeHelp(stdout, args[1:])
	}
	if args[0] == "companies" && (len(args) == 1 || isHelpFlag(args[1])) {
		return writeHelp(stdout, []string{"companies"})
	}
	dir := os.Getenv("LINKEDIN_CONFIG_DIR")
	if dir == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("find configuration directory: %w", err)
		}
		dir = filepath.Join(root, "linkedin")
	}
	store := authStore{dir: dir}
	switch args[0] {
	case "companies":
		if len(args) < 2 || args[1] != "people" {
			return fmt.Errorf("expected %q; run linkedin --help", args[0]+" people <company>")
		}
		fs := flag.NewFlagSet("companies people", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { _ = writeHelp(stderr, []string{"companies", "people"}) }
		limit := fs.Int("limit", pageSize, "maximum people to return")
		start := fs.Int("start", 0, "result offset")
		all := fs.Bool("all", false, "fetch all available people")
		positionals, err := parseFlags(fs, args[2:])
		if errors.Is(err, flag.ErrHelp) {
			return writeHelp(stdout, []string{"companies", "people"})
		}
		if err != nil {
			return err
		}
		if len(positionals) != 1 {
			return fmt.Errorf("companies people requires one company slug, ID, or URL; got %d arguments", len(positionals))
		}
		company, err := parseCompany(positionals[0])
		if err != nil {
			return err
		}
		if *limit < 1 || *start < 0 {
			return fmt.Errorf("--limit must be positive and --start nonnegative; got limit=%d start=%d", *limit, *start)
		}
		session, err := store.load()
		if err != nil {
			return err
		}
		httpClient := &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		}
		client := linkedinClient{http: httpClient, session: session}
		result, err := client.people(ctx, company, peopleOptions{limit: *limit, start: *start, all: *all})
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "auth":
		return runAuth(ctx, args[1:], store, stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; run linkedin --help", args[0])
	}
}

func runAuth(ctx context.Context, args []string, store authStore, stdout, stderr io.Writer) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		return writeHelp(stdout, []string{"auth"})
	}
	if len(args) == 2 && isHelpFlag(args[1]) {
		return writeHelp(stdout, []string{"auth", args[0]})
	}
	switch args[0] {
	case "login":
		fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { _ = writeHelp(stderr, []string{"auth", "login"}) }
		browserName := fs.String("browser", "auto", "browser to import (auto, chrome, arc, firefox, safari, etc.)")
		profile := fs.String("profile", "", "browser profile name, directory, or cookie database")
		cookieFile := fs.String("cookies-file", "", "import cookie JSON instead of a browser")
		positionals, err := parseFlags(fs, args[1:])
		if errors.Is(err, flag.ErrHelp) {
			return writeHelp(stdout, []string{"auth", "login"})
		}
		if err != nil {
			return err
		}
		if len(positionals) != 0 {
			return fmt.Errorf("auth login takes flags only; got %d positional arguments", len(positionals))
		}
		if *cookieFile != "" && (*browserName != "auto" || *profile != "") {
			return fmt.Errorf("--cookies-file cannot be combined with --browser or --profile")
		}
		browsers, err := selectBrowsers(*browserName)
		if err != nil {
			return err
		}
		if *profile != "" && *browserName == "auto" {
			return fmt.Errorf("--profile %q requires an explicit --browser", *profile)
		}
		session, warnings, err := importSession(ctx, browsers, *profile, *cookieFile)
		for _, warning := range warnings {
			fmt.Fprintln(stderr, "sweetcookie:", warning)
		}
		if err != nil {
			return err
		}
		if err := store.save(session); err != nil {
			return err
		}
		return writeJSON(stdout, session.status())
	case "status":
		if len(args) != 1 {
			return fmt.Errorf("auth status takes no arguments")
		}
		session, err := store.read()
		if errors.Is(err, os.ErrNotExist) {
			return writeJSON(stdout, authStatus{Reason: "no saved cookies; run linkedin auth login"})
		}
		if err != nil {
			return err
		}
		return writeJSON(stdout, session.status())
	case "logout":
		if len(args) != 1 {
			return fmt.Errorf("auth logout takes no arguments")
		}
		if err := os.Remove(store.path()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove saved cookies: %w", err)
		}
		return writeJSON(stdout, authStatus{Reason: "saved cookies removed"})
	default:
		return fmt.Errorf("unknown auth command %q; expected login, status, or logout", args[0])
	}
}

// flag.FlagSet stops at the first positional argument. Collect flags first so
// both `people --limit 12 example-company` and `people example-company --limit 12` work.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if isHelpFlag(arg) {
			return positionals, flag.ErrHelp
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}
		flags = append(flags, arg)
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		f := fs.Lookup(name)
		if f == nil || hasValue {
			continue
		}
		boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
		if (!ok || !boolean.IsBoolFlag()) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return positionals, fs.Parse(flags)
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write JSON output: %w", err)
	}
	return nil
}

func selectBrowsers(name string) ([]sweetcookie.Browser, error) {
	if name == "auto" {
		return sweetcookie.DefaultBrowsers(), nil
	}
	for _, b := range []sweetcookie.Browser{
		sweetcookie.BrowserChrome, sweetcookie.BrowserChromium, sweetcookie.BrowserEdge,
		sweetcookie.BrowserBrave, sweetcookie.BrowserVivaldi, sweetcookie.BrowserOpera,
		sweetcookie.BrowserArc, sweetcookie.BrowserHelium, sweetcookie.BrowserDia,
		sweetcookie.BrowserComet, sweetcookie.BrowserAtlas, sweetcookie.BrowserWhale,
		sweetcookie.BrowserFirefox, sweetcookie.BrowserZen, sweetcookie.BrowserFloorp,
		sweetcookie.BrowserWaterfox, sweetcookie.BrowserLibreWolf, sweetcookie.BrowserSafari,
	} {
		if string(b) == strings.ToLower(name) {
			return []sweetcookie.Browser{b}, nil
		}
	}
	return nil, fmt.Errorf("unsupported --browser %q; use auto, chrome, chromium, edge, brave, vivaldi, opera, arc, helium, dia, comet, atlas, whale, firefox, zen, floorp, waterfox, librewolf, or safari", name)
}
