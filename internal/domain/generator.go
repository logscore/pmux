package domain

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Generate builds a domain like "feat-auth.my-app.test".
// Every run always gets a subdomain:
//   - nameOverride if provided via --name
//   - git branch name if in a git repo
//   - stable 5-char hash of the cwd if not in git
func Generate(nameOverride string) (string, error) {
	root, err := rootDomain()
	if err != nil {
		return "", err
	}

	sub := nameOverride
	if sub == "" {
		sub = branchName()
	}
	if sub == "" {
		sub = hashDir()
	}

	return sanitize(sub) + "." + root + ".test", nil
}

// rootDomain returns the project name from the git worktree root directory.
// Falls back to the current directory name if not in a git repo.
func rootDomain() (string, error) {
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		return sanitize(filepath.Base(strings.TrimSpace(string(out)))), nil
	}

	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return sanitize(filepath.Base(dir)), nil
}

func branchName() string {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// hashDir returns a stable 5-char hex string derived from the cwd path.
func hashDir() string {
	dir, _ := os.Getwd()
	h := sha256.Sum256([]byte(dir))
	return fmt.Sprintf("%x", h[:3])[:5]
}

func sanitize(s string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9-]`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return strings.ToLower(s)
}
