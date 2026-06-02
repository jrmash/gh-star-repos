// Package settings loads extension settings from configuration files, the
// environment, and explicitly supplied command line arguments.
package settings

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config is the fully resolved and validated configuration for one invocation.
type Config struct {
	Owner          string            `json:"owner" yaml:"owner"`
	Archived       string            `json:"archived" yaml:"archived"`
	Forks          string            `json:"forks" yaml:"forks"`
	Format         string            `json:"format" yaml:"format"`
	Visibility     string            `json:"visibility" yaml:"visibility"`
	Language       string            `json:"language" yaml:"language"`
	Topics         []string          `json:"topic" yaml:"topic"`
	Exclude        []string          `json:"exclude" yaml:"exclude"`
	Lists          []string          `json:"list" yaml:"list"`
	CreateLists    bool              `json:"create-lists" yaml:"create-lists"`
	ListVisibility string            `json:"list-visibility" yaml:"list-visibility"`
	Sleep          int               `json:"sleep" yaml:"sleep"`
	MaxRepos       int               `json:"max-repos" yaml:"max-repos"`
	DryRun         bool              `json:"dry-run" yaml:"dry-run"`
	Sources        map[string]string `json:"-" yaml:"-"`
}

var names = []string{
	"owner", "archived", "forks", "format", "visibility", "language", "topic", "exclude", "list",
	"create-lists", "list-visibility", "sleep", "max-repos", "dry-run",
}

// Environment names are ordered from fallback aliases to the canonical name.
// Presence matters: an explicitly empty canonical value overrides an alias.
var environmentNames = map[string][]string{
	"owner":           {"GHSR_OWNER"},
	"archived":        {"GHSR_ARCHIVED", "GHSR_OPT_ARCHIVED"},
	"forks":           {"GHSR_FORKS", "GHSR_OPT_FORKS"},
	"format":          {"GHSR_FORMAT", "GHSR_OPT_FORMAT"},
	"visibility":      {"GHSR_VISIBILITY", "GHSR_OPT_VISIBILITY"},
	"language":        {"GHSR_LANGUAGE", "GHSR_OPT_LANGUAGE"},
	"topic":           {"GHSR_TOPIC", "GHSR_OPT_TOPIC"},
	"exclude":         {"GHSR_EXCLUDE", "GHSR_OPT_EXCLUDE"},
	"list":            {"GHSR_LIST", "GHSR_OPT_LIST"},
	"create-lists":    {"GHSR_CREATE_LISTS", "GHSR_OPT_CREATE_LISTS"},
	"list-visibility": {"GHSR_LIST_VISIBILITY", "GHSR_OPT_LIST_VISIBILITY"},
	"sleep":           {"GHSR_SLEEP", "GHSR_OPT_SLEEP"},
	"max-repos":       {"GHSR_MAX_REPOS", "GHSR_OPT_LIMIT"},
	"dry-run":         {"GHSR_DRY_RUN", "GHSR_OPT_DRY_RUN"},
}

var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type value struct {
	text   string
	list   []string
	array  bool
	source string
}

// Load resolves defaults < global YAML < local YAML < GHSR_* < overrides.
// Overrides contain only explicitly supplied flags; their keys match flag names.
// A nil lookup uses os.LookupEnv.
func Load(cwd string, lookup func(string) (string, bool), overrides map[string]string) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if cwd == "" {
		return Config{}, errors.New("invocation directory must not be empty")
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return Config{}, fmt.Errorf("resolve invocation directory: %w", err)
	}
	values := map[string]value{
		"owner": {}, "archived": {text: "any"}, "forks": {text: "exclude"},
		"format": {text: "human"}, "visibility": {text: "any"}, "language": {},
		"topic": {array: true}, "exclude": {array: true}, "list": {array: true},
		"create-lists": {text: "false"}, "list-visibility": {text: "private"},
		"sleep": {text: "1"}, "max-repos": {text: "0"},
		"dry-run": {text: "false"},
	}
	globalDir, err := configDir(lookup, runtime.GOOS, os.UserHomeDir)
	if err != nil {
		return Config{}, err
	}
	globalDir, err = resolvePath(globalDir, cwd, lookup)
	if err != nil {
		return Config{}, fmt.Errorf("resolve global configuration directory: %w", err)
	}
	for _, filename := range []string{filepath.Join(globalDir, "star-repos.yml"), filepath.Join(cwd, ".gh-star-repos.yml")} {
		layer, err := readFile(filename)
		if err != nil {
			return Config{}, err
		}
		for key, entry := range layer {
			values[key] = entry
		}
	}
	for _, key := range names {
		for _, env := range environmentNames[key] {
			if text, ok := lookup(env); ok {
				values[key] = value{text: text, source: env}
			}
		}
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return Config{}, fmt.Errorf("unknown command line setting %q", key)
		}
		source := "--" + key
		if key == "owner" {
			source = "positional owner"
		}
		values[key] = value{text: overrides[key], source: source}
	}
	return resolve(values)
}

func configDir(lookup func(string) (string, bool), goos string, home func() (string, error)) (string, error) {
	if dir, _ := lookup("GH_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if dir, _ := lookup("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "gh"), nil
	}
	if goos == "windows" {
		for _, key := range []string{"APPDATA", "AppData"} {
			if dir, _ := lookup(key); dir != "" {
				return filepath.Join(dir, "GitHub CLI"), nil
			}
		}
	}
	dir, _ := lookup("HOME")
	if dir == "" {
		var err error
		dir, err = home()
		if err != nil {
			return "", fmt.Errorf("find home directory for global configuration: %w", err)
		}
	}
	return filepath.Join(dir, ".config", "gh"), nil
}

func readFile(filename string) (map[string]value, error) {
	data, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read configuration %q: %w", filename, err)
	}
	layer, err := decodeYAML(string(data))
	if err != nil {
		return nil, fmt.Errorf("configuration %q: %w", filename, err)
	}
	for key, entry := range layer {
		entry.source = filename
		layer[key] = entry
	}
	return layer, nil
}

func decodeYAML(data string) (map[string]value, error) {
	decoder := yaml.NewDecoder(strings.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("expected one YAML document")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("expected a YAML mapping of setting names to values")
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	result := make(map[string]value, len(root.Content)/2)
	for i := 0; i < len(root.Content); i += 2 {
		key, node := root.Content[i], root.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || !allowed[key.Value] {
			return nil, fmt.Errorf("unknown setting %q", key.Value)
		}
		if _, exists := result[key.Value]; exists {
			return nil, fmt.Errorf("duplicate setting %q", key.Value)
		}
		entry, err := decodeValue(key.Value, node)
		if err != nil {
			return nil, err
		}
		result[key.Value] = entry
	}
	return result, nil
}

func decodeValue(key string, node *yaml.Node) (value, error) {
	if node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	switch key {
	case "topic", "exclude", "list":
		if node.Kind != yaml.SequenceNode {
			return value{}, fmt.Errorf("%s must be a YAML sequence of strings", key)
		}
		items := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
				return value{}, fmt.Errorf("%s must be a YAML sequence of strings", key)
			}
			items = append(items, item.Value)
		}
		return value{list: items, array: true}, nil
	case "sleep", "max-repos":
		if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
			return value{}, fmt.Errorf("%s must be a YAML integer", key)
		}
		var number int
		if err := node.Decode(&number); err != nil {
			return value{}, fmt.Errorf("%s must be an integer: %w", key, err)
		}
		return value{text: strconv.Itoa(number)}, nil
	case "dry-run", "create-lists":
		if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
			return value{}, fmt.Errorf("%s must be a YAML boolean", key)
		}
		return value{text: strings.ToLower(node.Value)}, nil
	default:
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			return value{}, fmt.Errorf("%s must be a YAML string", key)
		}
	}
	return value{text: node.Value}, nil
}

func resolve(values map[string]value) (Config, error) {
	cfg := Config{
		Owner: values["owner"].text, Archived: values["archived"].text,
		Forks: values["forks"].text, Visibility: values["visibility"].text,
		Language:       strings.TrimSpace(values["language"].text),
		ListVisibility: values["list-visibility"].text, Format: values["format"].text,
		Sources: map[string]string{},
	}
	invalid := func(key, reason string) error {
		if source := values[key].source; source != "" {
			return fmt.Errorf("%s: %s %s", source, key, reason)
		}
		return fmt.Errorf("%s %s", key, reason)
	}
	for key, entry := range values {
		cfg.Sources[key] = entry.source
		if entry.source == "" {
			cfg.Sources[key] = "default"
		}
	}
	if cfg.Owner != "" && !ownerPattern.MatchString(cfg.Owner) {
		return Config{}, invalid("owner", "must be a GitHub user or organization handle without paths, whitespace, or option prefixes")
	}
	for _, rule := range []struct{ key, allowed string }{
		{"archived", "any|exclude|only"}, {"forks", "any|exclude|only"},
		{"format", "human|json|yaml"}, {"visibility", "any|internal|private|public"},
		{"list-visibility", "private|public"},
	} {
		if !slices.Contains(strings.Split(rule.allowed, "|"), values[rule.key].text) {
			return Config{}, invalid(rule.key, "must be one of "+rule.allowed)
		}
	}
	var err error
	cfg.Sleep, err = strconv.Atoi(values["sleep"].text)
	if err != nil || cfg.Sleep < 0 || cfg.Sleep > 300 {
		return Config{}, invalid("sleep", "must be an integer from 0 to 300")
	}
	cfg.MaxRepos, err = strconv.Atoi(values["max-repos"].text)
	if err != nil || cfg.MaxRepos < 0 {
		return Config{}, invalid("max-repos", "must be a nonnegative integer (0 means unlimited)")
	}
	for _, field := range []struct {
		key string
		out *bool
	}{{"dry-run", &cfg.DryRun}, {"create-lists", &cfg.CreateLists}} {
		var err error
		*field.out, err = strconv.ParseBool(values[field.key].text)
		if err != nil {
			return Config{}, invalid(field.key, "must be a boolean")
		}
	}
	for _, field := range []struct {
		key string
		out *[]string
	}{{"topic", &cfg.Topics}, {"exclude", &cfg.Exclude}, {"list", &cfg.Lists}} {
		entry := values[field.key]
		items := entry.list
		if !entry.array {
			var err error
			items, err = ParseList(entry.text)
			if err != nil {
				return Config{}, invalid(field.key, "must be valid CSV: "+err.Error())
			}
		}
		*field.out = normalize(items)
	}
	for _, pattern := range cfg.Exclude {
		if _, err := path.Match(pattern, ""); err != nil {
			return Config{}, invalid("exclude", fmt.Sprintf("contains invalid glob %q", pattern))
		}
	}
	return cfg, nil
}

// ParseList reads the CSV syntax used by list settings. Empty input clears the
// setting. Whitespace and empty entries are removed; matching and case folding
// are left to repository filtering.
func ParseList(text string) ([]string, error) {
	if strings.TrimSpace(text) == "" {
		return []string{}, nil
	}
	r := csv.NewReader(strings.NewReader(text))
	r.TrimLeadingSpace = true
	items, err := r.Read()
	if err != nil {
		return nil, err
	}
	if _, err := r.Read(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("expected one CSV record")
	}
	return normalize(items), nil
}

func EncodeList(items []string) string {
	var buffer strings.Builder
	w := csv.NewWriter(&buffer)
	_ = w.Write(items)
	w.Flush()
	return strings.TrimSuffix(buffer.String(), "\n")
}

func normalize(items []string) []string {
	result := []string{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" && !slices.Contains(result, item) {
			result = append(result, item)
		}
	}
	return result
}

func resolvePath(path, cwd string, lookup func(string) (string, bool)) (string, error) {
	if strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("must not contain NUL characters")
	}
	if strings.HasPrefix(path, "~") {
		if path != "~" && !strings.HasPrefix(path, "~/") && !(runtime.GOOS == "windows" && strings.HasPrefix(path, `~\`)) {
			return "", errors.New("supports ~ for the current user's home directory, not ~user")
		}
		home, _ := lookup("HOME")
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("cannot find home directory: %w", err)
			}
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path), nil
}
