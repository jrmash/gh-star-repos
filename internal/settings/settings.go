// Package settings resolves defaults, YAML, environment, and explicit arguments.
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
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Owner          string            `json:"owner" yaml:"owner"`
	Archived       string            `json:"archived" yaml:"archived"`
	Forks          string            `json:"forks" yaml:"forks"`
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
	Format         string            `json:"format" yaml:"format"`
	Sources        map[string]string `json:"-" yaml:"-"`
}

var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

var keys = []string{
	"owner", "archived", "forks", "visibility", "language", "topic", "exclude", "list",
	"create-lists", "list-visibility", "sleep", "max-repos", "dry-run", "format",
}

type value struct {
	text   string
	list   []string
	array  bool
	source string
}

// Load only considers explicitly supplied flags in overrides. Empty, false,
// and zero are values, not indications that a setting was absent.
func Load(cwd string, lookup func(string) (string, bool), overrides map[string]string) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if cwd == "" {
		return Config{}, errors.New("invocation directory must not be empty")
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return Config{}, err
	}
	values := map[string]value{
		"owner": {}, "archived": {text: "any"}, "forks": {text: "exclude"},
		"visibility": {text: "any"}, "language": {},
		"topic": {array: true}, "exclude": {array: true}, "list": {array: true},
		"create-lists": {text: "false"}, "list-visibility": {text: "private"},
		"sleep": {text: "1"}, "max-repos": {text: "0"},
		"dry-run": {text: "false"}, "format": {text: "human"},
	}
	for key, entry := range values {
		entry.source = "default"
		values[key] = entry
	}
	dir, err := configDir(lookup, runtime.GOOS, os.UserHomeDir)
	if err != nil {
		return Config{}, err
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	for _, filename := range []string{filepath.Join(dir, "star-repos.yml"), filepath.Join(cwd, ".gh-star-repos.yml")} {
		layer, err := readFile(filename)
		if err != nil {
			return Config{}, fmt.Errorf("configuration %q: %w", filename, err)
		}
		for key, entry := range layer {
			entry.source = filename
			values[key] = entry
		}
	}
	for _, key := range keys {
		// Aliases are visited first; canonical names win even when empty.
		for _, name := range environmentNames(key) {
			if text, present := lookup(name); present {
				values[key] = value{text: text, source: name}
			}
		}
	}
	for key, text := range overrides {
		if _, ok := values[key]; !ok {
			return Config{}, fmt.Errorf("unknown setting %q", key)
		}
		source := "--" + key
		if key == "owner" {
			source = "positional owner"
		}
		values[key] = value{text: text, source: source}
	}
	return resolve(values)
}

func environmentNames(key string) []string {
	if key == "owner" {
		return []string{"GHSR_OWNER"}
	}
	if key == "max-repos" {
		return []string{"GHSR_MAX_REPOS", "GHSR_OPT_LIMIT"}
	}
	name := strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
	return []string{"GHSR_" + name, "GHSR_OPT_" + name}
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
			return "", fmt.Errorf("find home directory: %w", err)
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
		return nil, err
	}
	return decodeYAML(string(data))
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
		return nil, errors.New("expected one YAML document")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("expected a YAML mapping of settings")
	}
	result := map[string]value{}
	for index := 0; index < len(root.Content); index += 2 {
		key, node := root.Content[index], root.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || !slices.Contains(keys, key.Value) {
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
			return value{}, err
		}
		return value{text: strconv.Itoa(number)}, nil
	case "dry-run", "create-lists":
		if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
			return value{}, fmt.Errorf("%s must be a YAML boolean", key)
		}
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
		return fmt.Errorf("%s: %s %s", values[key].source, key, reason)
	}
	for key, entry := range values {
		cfg.Sources[key] = entry.source
	}
	if cfg.Owner != "" && !ownerPattern.MatchString(cfg.Owner) {
		return Config{}, invalid("owner", "must be a GitHub handle without paths or whitespace")
	}
	for _, rule := range []struct{ key, allowed string }{
		{"archived", "any|exclude|only"}, {"forks", "any|exclude|only"},
		{"visibility", "any|public|private|internal"}, {"format", "human|json|yaml"},
		{"list-visibility", "private|public"},
	} {
		if !slices.Contains(strings.Split(rule.allowed, "|"), values[rule.key].text) {
			return Config{}, invalid(rule.key, "must be one of "+rule.allowed)
		}
	}
	for _, field := range []struct {
		key string
		out *int
	}{{"sleep", &cfg.Sleep}, {"max-repos", &cfg.MaxRepos}} {
		number, err := strconv.Atoi(values[field.key].text)
		if err != nil || number < 0 {
			return Config{}, invalid(field.key, "must be a nonnegative integer")
		}
		*field.out = number
	}
	if int64(cfg.Sleep) > (1<<63-1)/int64(time.Second) {
		return Config{}, invalid("sleep", "exceeds the supported duration")
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
		return nil, errors.New("expected a single CSV record")
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
