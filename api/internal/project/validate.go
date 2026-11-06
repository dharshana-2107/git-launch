package project

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	// Public GitHub repos only for now: https://github.com/<owner>/<repo>
	repoURLPattern = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?(\.git)?/?$`)

	// A subdomain becomes part of a URL (<subdomain>.localhost), so it must be
	// a valid DNS label: lowercase letters, digits and hyphens, 1-32 chars,
	// not starting or ending with a hyphen.
	subdomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

	// Git branch names: letters, digits and . _ / - (a safe subset).
	branchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}$`)

	// Env var keys in the usual SHELL_STYLE form.
	envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

// Names the platform itself will use, so no project may claim them.
var reservedSubdomains = map[string]bool{
	"api": true, "www": true, "app": true, "traefik": true, "launchpad": true, "admin": true,
}

const maxEnvVars = 50

// Normalize trims whitespace and fills defaults before validation.
func (in *Input) Normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.RepoURL = strings.TrimSpace(in.RepoURL)
	in.Branch = strings.TrimSpace(in.Branch)
	in.Subdomain = strings.ToLower(strings.TrimSpace(in.Subdomain))
	if in.Branch == "" {
		in.Branch = "main"
	}
	if in.EnvVars == nil {
		in.EnvVars = map[string]string{}
	}
}

// Validate returns one message per bad field (empty map = valid).
// It collects every problem instead of stopping at the first, so the form
// can show all errors at once.
func (in Input) Validate() map[string]string {
	errs := map[string]string{}

	if in.Name == "" || len(in.Name) > 50 {
		errs["name"] = "name is required and must be at most 50 characters"
	}
	if !repoURLPattern.MatchString(in.RepoURL) {
		errs["repoUrl"] = "must be a public GitHub URL like https://github.com/owner/repo"
	}
	if !branchPattern.MatchString(in.Branch) {
		errs["branch"] = "branch may only contain letters, digits and . _ / -"
	}
	switch {
	case !subdomainPattern.MatchString(in.Subdomain):
		errs["subdomain"] = "use 1-32 lowercase letters, digits or hyphens, not starting or ending with a hyphen"
	case reservedSubdomains[in.Subdomain]:
		errs["subdomain"] = fmt.Sprintf("%q is reserved", in.Subdomain)
	}
	if len(in.EnvVars) > maxEnvVars {
		errs["envVars"] = fmt.Sprintf("at most %d environment variables", maxEnvVars)
	}
	for k := range in.EnvVars {
		if !envKeyPattern.MatchString(k) {
			errs["envVars"] = fmt.Sprintf("invalid key %q: use letters, digits and _, not starting with a digit", k)
			break
		}
	}
	return errs
}
