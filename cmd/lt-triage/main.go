// lt-triage files GitHub issues for load-test findings: nonzero flagged
// counters (minus the known-benign allowlist in triage.allowlist) and ramp
// trips with no counter evidence. It dedups against loadtest-finding issues
// by "[component] counter" title key — commenting on still-open matches,
// reopening closed matches as regressions — so the suite can run on a
// schedule without filing duplicates.
//
// Issues carry the repro command, evidence (counter values, err notes,
// endpoint status breakdown), and the fix contract: smallest-diff PR with
// tests, verified by re-dispatching this suite against the fix SHA plus all
// app-repo required checks.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/danielliu30/dating-coach-loadtest/internal/harness"
)

type finding struct {
	Component string
	Key       string // dedup key, e.g. "[lt-chat] fan_send_failed"
	Title     string
	Body      string
}

// allowlist maps "component:counter-substring" — flagged counters matching
// an entry are known-benign (teardown artifacts, expected-assertion
// counters) or tracked elsewhere, and must not file issues.
func allowlist(path string) (map[string][]string, error) {
	out := map[string][]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.Split(line, "#")[0]
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		comp, sub, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("allowlist line %q: want component:counter-substring", line)
		}
		out[strings.TrimSpace(comp)] = append(out[strings.TrimSpace(comp)], strings.TrimSpace(sub))
	}
	return out, nil
}

func allowed(allow map[string][]string, comp, counter string) bool {
	for _, sub := range allow[comp] {
		if strings.Contains(counter, sub) {
			return true
		}
	}
	return false
}

// lastGood mirrors lt-summarize: find the ramp result in report extras.
func rampState(extra map[string]any) (good string, tripped bool) {
	good = "-"
	for _, raw := range extra {
		data, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var r struct {
			LastGoodStage *struct {
				Name string `json:"Name"`
			} `json:"last_good_stage"`
			Tripped bool `json:"tripped"`
		}
		if err := json.Unmarshal(data, &r); err != nil || r.LastGoodStage == nil {
			continue
		}
		if r.LastGoodStage.Name != "" {
			good = r.LastGoodStage.Name
		}
		tripped = tripped || r.Tripped
	}
	return good, tripped
}

// errNotes pulls the "err: ..." failure details Failf/Errf recorded into
// the report — the actual error behind each counter.
func errNotes(rep *harness.Report, cap int) []string {
	var out []string
	for _, n := range rep.Notes {
		if strings.HasPrefix(n, "err: ") {
			out = append(out, strings.TrimPrefix(n, "err: "))
		}
	}
	if len(out) > cap {
		out = out[:cap]
	}
	return out
}

// endpointFailures summarizes per-operation statuses that are not 2xx —
// which operation failed, and with what.
func endpointFailures(rep *harness.Report, cap int) []string {
	var out []string
	for name, e := range rep.Endpoints {
		var bad []string
		for status, n := range e.Statuses {
			if status >= 500 || status <= 0 { // 5xx or transport error
				bad = append(bad, fmt.Sprintf("%d×%d", status, n))
			}
		}
		if len(bad) > 0 {
			out = append(out, fmt.Sprintf("- `%s`: %s", name, strings.Join(bad, ", ")))
		}
	}
	sort.Strings(out)
	if len(out) > cap {
		out = out[:cap]
	}
	return out
}

func body(rep *harness.Report, runURL, counterBlock string, notes, epFails []string, good string, tripped bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Surfaced by the load-test suite (`%s`).\n\n", rep.Component)
	fmt.Fprintf(&b, "**Run:** %s\n", runURL)
	fmt.Fprintf(&b, "**Counters:** %s\n", counterBlock)
	fmt.Fprintf(&b, "**Ramp:** last_good=%s tripped=%v\n", good, tripped)
	if len(epFails) > 0 {
		b.WriteString("\n### Per-operation failures\n\n")
		for _, l := range epFails {
			b.WriteString(l + "\n")
		}
	}
	if len(notes) > 0 {
		b.WriteString("\n### Error details\n\n```\n")
		for _, n := range notes {
			b.WriteString(n + "\n")
		}
		b.WriteString("```\n")
	}
	b.WriteString(`
### Repro

` + "```sh" + `
# from the dating-coach-loadtest checkout, app stack up per README
docker compose --env-file .env -f app/docker-compose.yml -f docker-compose.override.yml --profile gateway up -d --build
LT_BASE=http://localhost go run ./cmd/` + rep.Component + ` -vus 10,20,50 -rps 150 -duration 30s
` + "```" + `

### Fix contract

- Investigate root cause in the app service this component exercises (compose logs are attached to the run's artifacts).
- Smallest-diff PR against danielliu30/dating-coach with tests in the same PR, per repo guardrails.
- **Verification:** re-run this suite against the fix's head SHA — gh workflow run load-test.yml --repo danielliu30/dating-coach-loadtest -f app_sha=<sha> — plus all app-repo required checks (build-test, typecheck-export, test, e2e, dependency-gate, merge-gate-tests). Do not merge on app tests alone; the load finding must not recur.
`)
	return b.String()
}

func collect(out string, allow map[string][]string, runURL string) ([]finding, error) {
	latest, err := harness.LatestReports(out)
	if err != nil {
		return nil, err
	}
	comps := make([]string, 0, len(latest))
	for c := range latest {
		comps = append(comps, c)
	}
	sort.Strings(comps)

	var findings []finding
	for _, c := range comps {
		rep, err := harness.ReadReport(latest[c])
		if err != nil {
			continue
		}
		good, tripped := rampState(rep.Extra)
		notes := errNotes(rep, 8)
		epFails := endpointFailures(rep, 6)

		flagged := harness.Flagged(rep.Counters)
		keys := make([]string, 0, len(flagged))
		for k := range flagged {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var filed []string
		for _, k := range keys {
			if allowed(allow, c, k) {
				continue
			}
			counterBlock := fmt.Sprintf("`%s=%d`", k, flagged[k])
			findings = append(findings, finding{
				Component: c,
				Key:       fmt.Sprintf("[%s] %s", c, k),
				Title:     fmt.Sprintf("[%s] %s surfaced under load", c, k),
				Body:      body(rep, runURL, counterBlock, notes, epFails, good, tripped),
			})
			filed = append(filed, k)
		}

		// A trip with no flagged counters at all is a capacity finding the
		// counters didn't name (e.g. tripwire 5xx saturation).
		if tripped && len(flagged) == 0 {
			findings = append(findings, finding{
				Component: c,
				Key:       fmt.Sprintf("[%s] ramp tripped", c),
				Title:     fmt.Sprintf("[%s] ramp tripped at stage boundary under load", c),
				Body:      body(rep, runURL, "_(tripwire, no flagged counters)_", notes, epFails, good, tripped),
			})
		}
		_ = filed
	}
	return findings, nil
}

type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
}

func gh(args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func main() {
	out := flag.String("out", "out", "report directory")
	allowPath := flag.String("allow", "triage.allowlist", "known-benign counter allowlist")
	repo := flag.String("repo", os.Getenv("LT_ISSUE_REPO"), "issue repo (owner/name); default: this repo")
	runURL := flag.String("run", os.Getenv("RUN_URL"), "workflow run URL to link")
	dryRun := flag.Bool("dry-run", false, "print findings without touching GitHub")
	flag.Parse()
	if *runURL == "" {
		*runURL = "(local run)"
	}
	if *repo == "" {
		*repo = "danielliu30/dating-coach-loadtest"
	}

	allow, err := allowlist(*allowPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "allowlist:", err)
		os.Exit(1)
	}
	findings, err := collect(*out, allow, *runURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "collect:", err)
		os.Exit(1)
	}
	if len(findings) == 0 {
		fmt.Println("triage: no findings")
		return
	}
	fmt.Printf("triage: %d finding(s)\n", len(findings))
	if *dryRun {
		for _, f := range findings {
			fmt.Println(" -", f.Title)
		}
		return
	}

	// One label + open/closed issue listing up front.
	gh("label", "create", "loadtest-finding", "--color", "B60205",
		"--description", "Surfaced by the load-test suite", "--force", "--repo", *repo)
	res, err := gh("issue", "list", "--repo", *repo, "--label", "loadtest-finding",
		"--state", "all", "--limit", "500", "--json", "number,title,state")
	if err != nil {
		fmt.Fprintln(os.Stderr, "issue list:", err)
		os.Exit(1)
	}
	var issues []ghIssue
	json.Unmarshal([]byte(res), &issues)

	for _, f := range findings {
		var match *ghIssue
		for i := range issues {
			if strings.HasPrefix(issues[i].Title, f.Key+" ") || issues[i].Title == f.Key {
				match = &issues[i]
				break
			}
		}
		switch {
		case match == nil:
			if out, err := gh("issue", "create", "--repo", *repo, "--label", "loadtest-finding",
				"--title", f.Title, "--body", f.Body); err != nil {
				fmt.Fprintf(os.Stderr, "create %q: %v\n%s\n", f.Key, err, out)
				continue
			}
			fmt.Println("filed:", f.Key)
		case match.State == "OPEN":
			gh("issue", "comment", fmt.Sprint(match.Number), "--repo", *repo,
				"--body", fmt.Sprintf("Recurred in %s", *runURL))
			fmt.Println("still open:", f.Key, "#"+fmt.Sprint(match.Number))
		default: // CLOSED — the counter came back: regression
			gh("issue", "reopen", fmt.Sprint(match.Number), "--repo", *repo)
			gh("issue", "comment", fmt.Sprint(match.Number), "--repo", *repo,
				"--body", fmt.Sprintf("**Regression** — recurred in %s", *runURL))
			fmt.Println("regression, reopened:", f.Key, "#"+fmt.Sprint(match.Number))
		}
	}
}
