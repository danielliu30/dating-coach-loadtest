package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SeedPassword is the single password every seeded account shares; its bcrypt
// hash is produced by Postgres's crypt() so seeding stays pure SQL.
const SeedPassword = "loadtest-password"

// Tag builds a per-run fixture tag ("component-<time>-<pid>") so concurrent
// components never share the CleanupTag prefix — one run's cleanup can never
// delete another run's seeded accounts mid-flight.
func Tag(component string) string {
	return fmt.Sprintf("%s-%x-%d", component, time.Now().UnixNano()&0xffffff, os.Getpid()%0x1000)
}

// SuiteRoot walks up from the working directory for docker-compose.override.yml
// — the file only this repo ships — so components work from any cwd.
func SuiteRoot() (string, error) {
	dir, err := filepath.Abs(".")
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "docker-compose.override.yml")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no docker-compose.override.yml found walking up from cwd")
		}
		dir = parent
	}
}

// AppRoot returns the dating-coach checkout that owns docker-compose.yml:
// LT_APP_DIR when set, else the sibling checkout ../dating-coach that the
// README's clone layout produces.
func AppRoot() (string, error) {
	if dir := os.Getenv("LT_APP_DIR"); dir != "" {
		return filepath.Abs(dir)
	}
	suite, err := SuiteRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(suite, "..", "dating-coach")
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
		return "", fmt.Errorf("no docker-compose.yml at %s: set LT_APP_DIR to the dating-coach checkout", dir)
	}
	return filepath.Abs(dir)
}

// composeArgs builds the `docker compose` prefix every helper shares: the
// app's compose file under the suite's env and override, so calls work from
// anywhere once the two checkouts exist.
func composeArgs(extra ...string) ([]string, error) {
	suite, err := SuiteRoot()
	if err != nil {
		return nil, err
	}
	app, err := AppRoot()
	if err != nil {
		return nil, err
	}
	args := []string{
		"compose", "--env-file", filepath.Join(suite, ".env"),
		"-f", filepath.Join(app, "docker-compose.yml"),
		"-f", filepath.Join(suite, "docker-compose.override.yml"),
	}
	return append(args, extra...), nil
}

// Psql runs one statement in the compose postgres container and returns
// tuples-only output.
func Psql(sql string) (string, error) {
	args, err := composeArgs("exec", "-T", "postgres", "psql", "-U", "datingcoach", "-d", "datingcoach", "-tAc", sql)
	if err != nil {
		return "", err
	}
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("psql failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ComposeLogs returns `docker compose logs` output for a service, used to
// scrape verification codes (dev mode, no SMTP) and to count backend warnings
// such as "dropping chat event for slow client".
func ComposeLogs(service, since string) (string, error) {
	args, err := composeArgs("logs", service, "--since", since, "--no-log-prefix")
	if err != nil {
		return "", err
	}
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("compose logs %s failed: %w", service, err)
	}
	return string(out), nil
}

// ComposeUp runs `docker compose up -d --wait` for the given services with the
// loadtest env file and override, used by lt-payments to bring the
// payments-enabled api-pay/worker-pay services up next to the base stack.
func ComposeUp(services ...string) error {
	args, err := composeArgs("up", "-d", "--wait")
	if err != nil {
		return err
	}
	args = append(args, services...)
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("compose up %v failed: %w: %s", services, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CountLogLines greps service logs for a substring and returns the number of
// matching lines — e.g. "dropping chat event for slow client".
func CountLogLines(service, since, needle string) (int, error) {
	logs, err := ComposeLogs(service, since)
	if err != nil {
		return 0, err
	}
	return strings.Count(logs, needle), nil
}

var verifyCodeRe = regexp.MustCompile(`Your verification code is: (\d{6})`)

// VerificationCode scrapes the api container logs for the six-digit code most
// recently emailed to email — dev mode logs the email body when SMTP is off.
func VerificationCode(email string, timeout time.Duration) (string, error) {
	needle := `"to":` + `"` + email + `"`
	deadline := time.Now().Add(timeout)
	for {
		logs, err := ComposeLogs("api", "5m")
		if err != nil {
			return "", err
		}
		var code string
		for _, line := range strings.Split(logs, "\n") {
			if !strings.Contains(line, needle) {
				continue
			}
			if m := verifyCodeRe.FindStringSubmatch(line); m != nil {
				code = m[1]
			}
		}
		if code != "" {
			return code, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no verification code for %s within %s", email, timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// SeedUsers inserts n verified accounts with emails tag-<i>@loadtest.local
// (i starting at from) and one shared password; it returns the emails.
// bcrypt hashing is done by Postgres's crypt() at cost 4 so seeding hundreds
// of accounts is fast.
func SeedUsers(tag string, from, n int, role string) ([]string, error) {
	sql := fmt.Sprintf(`INSERT INTO users (email, password_hash, display_name, role, email_verified)
SELECT '%s-' || g || '@loadtest.local', crypt('%s', gen_salt('bf', 4)), 'LT ' || '%s' || ' ' || g, '%s', true
FROM generate_series(%d, %d) AS g
RETURNING email`, tag, SeedPassword, tag, role, from, from+n-1)
	out, err := Psql(sql)
	if err != nil {
		return nil, err
	}
	var emails []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "@") {
			emails = append(emails, strings.TrimSpace(line))
		}
	}
	return emails, nil
}

// SeedCoach creates one approved coach with all-week availability; it returns
// the coach's user_id.
func SeedCoach(tag string) (string, error) {
	email := tag + "-coach@loadtest.local"
	sql := fmt.Sprintf(`WITH u AS (
  INSERT INTO users (email, password_hash, display_name, role, email_verified)
  VALUES ('%s', crypt('%s', gen_salt('bf', 4)), 'LT Coach %s', 'coach', true)
  RETURNING id
), c AS (
  INSERT INTO coaches (user_id, headline, bio, specialties, hourly_rate_cents, timezone, years_experience, accepting_clients, approval_status)
  SELECT id, 'LT coach', 'load test coach', '{openers,texting}', 12000, 'UTC', 3, true, 'approved' FROM u
  RETURNING user_id
), a AS (
  INSERT INTO coach_availability (coach_id, weekday, start_minute, end_minute)
  SELECT user_id, wd, 0, 1440 FROM c, generate_series(0,6) AS wd
)
SELECT user_id FROM c`, email, SeedPassword, tag)
	out, err := Psql(sql)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// MarkVerified flips email_verified for every account matching the tag —
// faster than scraping per-account codes for bulk fixtures.
func MarkVerified(tag string) error {
	_, err := Psql(fmt.Sprintf(`UPDATE users SET email_verified = true WHERE email LIKE '%s%%@loadtest.local'`, tag))
	return err
}

// CleanupTag deletes every user whose email starts with the tag; all child
// rows cascade.
func CleanupTag(tag string) error {
	_, err := Psql(fmt.Sprintf(`DELETE FROM users WHERE email LIKE '%s%%@loadtest.local'`, tag))
	return err
}
