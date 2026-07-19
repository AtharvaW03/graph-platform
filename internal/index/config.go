package index

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// validRepoName restricts Repository.Name to characters safe as a filesystem
// path segment and Neo4j string property, rejecting things like "../bad" or
// "/etc/passwd".
var validRepoName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// validBranch restricts Repository.Branch to safe git ref names; no leading
// '-' means a configured branch can never be parsed as a git option flag.
var validBranch = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]*$`)

// Config is the indexer's on-disk configuration. It declares the repository
// manifest and tunables for the git, graphify, and orchestrator subsystems.
type Config struct {
	Repositories []Repository     `yaml:"repositories"`
	Discovery    DiscoveryConfig  `yaml:"discovery"`
	Git          GitConfig        `yaml:"git"`
	Graphify     GraphifyConfig   `yaml:"graphify"`
	Extractors   ExtractorsConfig `yaml:"extractors"`
	Org          OrgConfig        `yaml:"org"`
}

// DiscoveryConfig enables GitHub App installation-based repository
// discovery: the manifest becomes "every repo the App is installed on",
// refreshed at most every TTL, merged with any static `repositories:`
// entries (static wins by name, e.g. to pin a non-default branch). Requires
// the GITHUB_APP_* environment variables. With discovery enabled the static
// repositories list may be empty.
type DiscoveryConfig struct {
	Enabled bool `yaml:"enabled"`
	// TTL caps how often the GitHub API is consulted; between refreshes the
	// cached manifest is served. Defaults to 10m.
	TTL time.Duration `yaml:"ttl"`
}

// ExtractorsConfig toggles each platform extractor on/off. All default to
// enabled - operators can disable any one by setting it to false.
type ExtractorsConfig struct {
	Deps    *bool `yaml:"deps"`
	HTTPAPI *bool `yaml:"http_api"`
	Kafka   *bool `yaml:"kafka"`
	MSSQL   *bool `yaml:"mssql"`
	Glue    *bool `yaml:"glue"`
	// MaxParallel caps concurrent extractors per repo. Zero or negative means
	// run all configured extractors at once.
	MaxParallel int `yaml:"max_parallel"`
	// AllowPartial controls what happens when an enabled extractor errors
	// for a repo. Default false: the repo's indexing run fails and nothing
	// imports. Set true to import the partial graph anyway.
	AllowPartial *bool `yaml:"allow_partial"`
}

// OrgConfig captures org-wide conventions used by extractors. When a
// dependency's name starts with one of Prefixes, the deps extractor emits a
// cross-repository edge, so "which repos depend on X" becomes a one-hop query.
type OrgConfig struct {
	Prefixes []string `yaml:"prefixes"`
}

// boolDefault dereferences an opt-out *bool, returning the default when the
// pointer is nil (operator left the YAML field empty).
func boolDefault(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

// DepsEnabled reports whether the deps extractor should run.
func (c ExtractorsConfig) DepsEnabled() bool    { return boolDefault(c.Deps, true) }
func (c ExtractorsConfig) HTTPAPIEnabled() bool { return boolDefault(c.HTTPAPI, true) }
func (c ExtractorsConfig) KafkaEnabled() bool   { return boolDefault(c.Kafka, true) }
func (c ExtractorsConfig) MSSQLEnabled() bool   { return boolDefault(c.MSSQL, true) }
func (c ExtractorsConfig) GlueEnabled() bool    { return boolDefault(c.Glue, true) }

// AllowPartialEnabled reports whether an extractor error should be tolerated
// (import the partial graph anyway) rather than failing the repo closed.
// Defaults to false: fail closed.
func (c ExtractorsConfig) AllowPartialEnabled() bool { return boolDefault(c.AllowPartial, false) }

// GitConfig tunes the GitSyncer.
type GitConfig struct {
	Timeout time.Duration `yaml:"timeout"`
}

// GraphifyConfig tunes the ExecGraphifier. Args supports the {repo_path}
// placeholder; OutputFile is interpreted relative to it.
type GraphifyConfig struct {
	Command    string        `yaml:"command"`
	Args       []string      `yaml:"args"`
	OutputFile string        `yaml:"output_file"`
	Timeout    time.Duration `yaml:"timeout"`
	// ExpectedVersion, if set, is compared against `<command> --version` once
	// at startup; a mismatch or failed detection hard-fails before any repo is
	// processed. Empty means "log whatever's detected and continue" - useful
	// while a fleet is still converging on one pinned version.
	ExpectedVersion string `yaml:"expected_version"`
	// IgnorePatterns are appended to every checkout's .graphifyignore
	// before extraction. Defaults to *.tfvars. Omitting the field keeps the
	// default; an explicit empty list ([]) disables injection.
	IgnorePatterns []string `yaml:"ignore_patterns"`
}

// DefaultConfig returns defaults for fields ApplyDefaults fills in on a
// loaded Config.
func DefaultConfig() Config {
	return Config{
		Git: GitConfig{
			Timeout: 10 * time.Minute,
		},
		Graphify: GraphifyConfig{
			Command: "graphify",
			// extract --code-only runs local AST extraction only (no LLM).
			// --force is required: it purges excluded files and bypasses
			// graphify's anti-shrink guard so a legitimately smaller repo
			// imports correctly. The importer owns correctness of the graph.
			Args:           []string{"extract", "{repo_path}", "--code-only", "--force"},
			OutputFile:     "graphify-out/graph.json",
			Timeout:        20 * time.Minute,
			IgnorePatterns: []string{"*.tfvars"},
		},
	}
}

// LoadConfig reads a YAML config file, fills in defaults, and validates it.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &c, nil
}

// ApplyDefaults fills any zero-valued field with the value from DefaultConfig.
// Existing user values are preserved.
func (c *Config) ApplyDefaults() {
	d := DefaultConfig()
	if c.Git.Timeout == 0 {
		c.Git.Timeout = d.Git.Timeout
	}
	if c.Graphify.Command == "" {
		c.Graphify.Command = d.Graphify.Command
	}
	if len(c.Graphify.Args) == 0 {
		c.Graphify.Args = d.Graphify.Args
	}
	if c.Graphify.OutputFile == "" {
		c.Graphify.OutputFile = d.Graphify.OutputFile
	}
	if c.Graphify.Timeout == 0 {
		c.Graphify.Timeout = d.Graphify.Timeout
	}
	// nil = field omitted, take the default; a non-nil empty list is an
	// explicit opt-out of ignore injection and is preserved.
	if c.Graphify.IgnorePatterns == nil {
		c.Graphify.IgnorePatterns = d.Graphify.IgnorePatterns
	}
	if c.Discovery.TTL == 0 {
		c.Discovery.TTL = 10 * time.Minute
	}
}

// Validate checks the loaded config before any repository is touched.
func (c *Config) Validate() error {
	if len(c.Repositories) == 0 && !c.Discovery.Enabled {
		return fmt.Errorf("no repositories configured (set discovery.enabled: true to source the manifest from the GitHub App installation instead)")
	}
	if c.Discovery.Enabled && c.Discovery.TTL < time.Minute {
		return fmt.Errorf("discovery.ttl must be at least 1m, got %s", c.Discovery.TTL)
	}
	seen := map[string]bool{}
	for i, r := range c.Repositories {
		if r.Name == "" {
			return fmt.Errorf("repositories[%d]: missing name", i)
		}
		if !validRepoName.MatchString(r.Name) {
			return fmt.Errorf("repositories[%d]: invalid name %q (must match %s; no path separators, spaces, or leading dot/dash)", i, r.Name, validRepoName.String())
		}
		if r.URL == "" {
			return fmt.Errorf("repositories[%d] (%s): missing url", i, r.Name)
		}
		if r.Branch == "" {
			return fmt.Errorf("repositories[%d] (%s): missing branch", i, r.Name)
		}
		if !validBranch.MatchString(r.Branch) {
			return fmt.Errorf("repositories[%d] (%s): invalid branch %q (must match %s; no leading dash or spaces)", i, r.Name, r.Branch, validBranch.String())
		}
		if seen[r.Name] {
			return fmt.Errorf("repositories: duplicate name %q", r.Name)
		}
		seen[r.Name] = true
	}
	return nil
}

// ConfigJobSource is the default JobSource: it serves the static repository
// list from a Config struct.
type ConfigJobSource struct {
	cfg *Config
}

func NewConfigJobSource(cfg *Config) *ConfigJobSource {
	return &ConfigJobSource{cfg: cfg}
}

func (s *ConfigJobSource) Repositories(_ context.Context) ([]Repository, error) {
	out := make([]Repository, len(s.cfg.Repositories))
	copy(out, s.cfg.Repositories)
	return out, nil
}
