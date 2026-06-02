package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	cwd, global := filepath.Join(root, "project"), filepath.Join(root, "global")
	for _, dir := range []string{cwd, global} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return cwd, map[string]string{"GH_CONFIG_DIR": global, "HOME": root}
}

func lookup(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { v, ok := env[key]; return v, ok }
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaults(t *testing.T) {
	cwd, env := fixture(t)
	cfg, err := Load(cwd, lookup(env), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Owner != "" || cfg.Archived != "any" || cfg.Forks != "exclude" || cfg.Visibility != "any" || cfg.Sleep != 1 || cfg.ListVisibility != "private" || cfg.Format != "human" || cfg.CreateLists || cfg.DryRun {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.Topics == nil || cfg.Exclude == nil || cfg.Lists == nil || len(cfg.Sources) != len(names) {
		t.Fatalf("missing arrays or source metadata: %+v", cfg)
	}
}

func TestAllLayersAndExplicitEmptyValues(t *testing.T) {
	cwd, env := fixture(t)
	global := filepath.Join(env["GH_CONFIG_DIR"], "star-repos.yml")
	local := filepath.Join(cwd, ".gh-star-repos.yml")
	write(t, global, "owner: global\narchived: exclude\nforks: only\nformat: yaml\nlanguage: Rust\ntopic: [global]\nlist: [global]\ndry-run: true\nsleep: 9\ncreate-lists: true\n")
	write(t, local, "owner: local\nforks: any\nformat: json\nlanguage: Go\ntopic: [local]\nlist: [local]\ndry-run: false\nsleep: 5\n")
	env["GHSR_OWNER"] = "environment"
	env["GHSR_DRY_RUN"], env["GHSR_OPT_DRY_RUN"] = "true", "false"
	env["GHSR_SLEEP"], env["GHSR_OPT_SLEEP"] = "2", "0"
	env["GHSR_TOPIC"], env["GHSR_OPT_TOPIC"] = "alias", ""
	env["GHSR_OPT_LIST"] = `"Go, CLI",Tools`
	env["GHSR_OPT_LANGUAGE"] = ""
	cfg, err := Load(cwd, lookup(env), map[string]string{"owner": "cli", "create-lists": "false"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Owner != "cli" || cfg.DryRun || cfg.CreateLists || cfg.Sleep != 0 || cfg.Language != "" || len(cfg.Topics) != 0 || !reflect.DeepEqual(cfg.Lists, []string{"Go, CLI", "Tools"}) {
		t.Fatalf("precedence failure: %+v", cfg)
	}
	if cfg.Archived != "exclude" || cfg.Forks != "any" || cfg.Format != "json" || cfg.Sources["archived"] != global || cfg.Sources["forks"] != local || cfg.Sources["owner"] != "positional owner" || cfg.Sources["sleep"] != "GHSR_OPT_SLEEP" {
		t.Fatalf("source failure: %+v", cfg.Sources)
	}
}

func TestEnvironmentForEverySetting(t *testing.T) {
	values := map[string]string{
		"owner": "octocat", "archived": "only", "forks": "any", "visibility": "internal",
		"language": "Go", "topic": "cli,tools", "exclude": "test-*,*-old", "list": "Go,Tools",
		"create-lists": "true", "list-visibility": "public", "sleep": "0", "max-repos": "12", "dry-run": "true", "format": "yaml",
	}
	for _, key := range names {
		t.Run(key, func(t *testing.T) {
			cwd, env := fixture(t)
			names := environmentNames[key]
			for _, name := range names {
				env[name] = values[key]
				cfg, err := Load(cwd, lookup(env), nil)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.Sources[key] != name {
					t.Fatalf("source = %q, want %q", cfg.Sources[key], name)
				}
				encoded, _ := json.Marshal(cfg)
				var fields map[string]any
				_ = json.Unmarshal(encoded, &fields)
				if fields[key] == nil {
					t.Fatalf("setting missing: %s", encoded)
				}
			}
		})
	}
}

func TestInvalidConfiguration(t *testing.T) {
	for _, data := range []string{
		"unknown: true", "owner: first\nowner: second", "owner: [a]", "topic: go", "list: [1]",
		"sleep: '1'", "dry-run: 'false'", "dry-run: null", "archived: false", "- owner",
		"owner: valid\n---\nowner: another", "owner: [", "{}\n---", "\n---\n",
	} {
		t.Run(data, func(t *testing.T) {
			cwd, env := fixture(t)
			write(t, filepath.Join(cwd, ".gh-star-repos.yml"), data)
			if _, err := Load(cwd, lookup(env), nil); err == nil {
				t.Fatal("invalid YAML accepted")
			}
		})
	}
	for key, text := range map[string]string{
		"owner": "org/repo", "archived": "true", "forks": "no", "visibility": "secret", "format": "xml",
		"list-visibility": "internal", "sleep": "-1", "max-repos": "-1", "dry-run": "", "create-lists": "perhaps", "exclude": "[bad", "topic": "\"unclosed",
	} {
		t.Run(key, func(t *testing.T) {
			cwd, env := fixture(t)
			_, err := Load(cwd, lookup(env), map[string]string{key: text})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("want sourced validation error, got %v", err)
			}
		})
	}
}

func TestValidationAfterPrecedence(t *testing.T) {
	cwd, env := fixture(t)
	env["GHSR_DRY_RUN"], env["GHSR_OPT_DRY_RUN"] = "invalid alias", "also invalid"
	if cfg, err := Load(cwd, lookup(env), map[string]string{"dry-run": "false"}); err != nil || cfg.DryRun {
		t.Fatalf("overridden value validated: %+v %v", cfg, err)
	}
}

func TestOnlyInvocationDirectory(t *testing.T) {
	cwd, env := fixture(t)
	write(t, filepath.Join(filepath.Dir(cwd), ".gh-star-repos.yml"), "owner: parent")
	write(t, filepath.Join(cwd, ".gh-star-repos.yaml"), "owner: alternate")
	cfg, err := Load(cwd, lookup(env), nil)
	if err != nil || cfg.Owner != "" {
		t.Fatalf("unexpected local config traversal: %+v %v", cfg, err)
	}
}

func TestConfigDirectoryPrecedence(t *testing.T) {
	env := map[string]string{"GH_CONFIG_DIR": "/custom", "XDG_CONFIG_HOME": "/xdg", "APPDATA": "/app", "HOME": "/user"}
	for _, tc := range []struct{ remove, goos, want string }{
		{"", "windows", "/custom"}, {"GH_CONFIG_DIR", "windows", "/xdg/gh"},
		{"XDG_CONFIG_HOME", "windows", "/app/GitHub CLI"}, {"APPDATA", "windows", "/user/.config/gh"},
	} {
		delete(env, tc.remove)
		got, err := configDir(lookup(env), tc.goos, func() (string, error) { return "", errors.New("unused") })
		if err != nil || filepath.Clean(got) != filepath.FromSlash(tc.want) {
			t.Fatalf("configDir = %q %v, want %q", got, err, tc.want)
		}
	}
}

func TestCSVAndClearing(t *testing.T) {
	for _, items := range [][]string{nil, {}, {"Go, CLI", "Tools"}, {"[a-z]*", "a\"b"}} {
		got, err := ParseList(EncodeList(items))
		if err != nil || !reflect.DeepEqual(got, normalize(items)) {
			t.Fatalf("CSV round trip: %q -> %q: %v", items, got, err)
		}
	}
	cwd, env := fixture(t)
	write(t, filepath.Join(cwd, ".gh-star-repos.yml"), "topic: [cli]\nexclude: [test-*]\nlist: [Go]\n")
	cfg, err := Load(cwd, lookup(env), map[string]string{"topic": "", "exclude": "", "list": ""})
	if err != nil || len(cfg.Topics)+len(cfg.Exclude)+len(cfg.Lists) != 0 {
		t.Fatalf("sequences did not clear: %+v %v", cfg, err)
	}
}

func TestGlobalConfigurationPaths(t *testing.T) {
	for _, tc := range []struct{ variable, directory, relative string }{
		{"GH_CONFIG_DIR", "relative-config", "relative-config"},
		{"GH_CONFIG_DIR", "~/config", "config"},
		{"XDG_CONFIG_HOME", "relative-config", "relative-config/gh"},
		{"XDG_CONFIG_HOME", "~/config", "config/gh"},
	} {
		t.Run(tc.variable+"/"+tc.directory, func(t *testing.T) {
			cwd, env := fixture(t)
			delete(env, "GH_CONFIG_DIR")
			env[tc.variable] = tc.directory
			base := cwd
			if strings.HasPrefix(tc.directory, "~") {
				base = env["HOME"]
			}
			dir := filepath.Join(base, filepath.FromSlash(tc.relative))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(dir, "star-repos.yml")
			write(t, filename, "owner: configured\n")
			cfg, err := Load(cwd, lookup(env), nil)
			if err != nil || cfg.Owner != "configured" || cfg.Sources["owner"] != filename {
				t.Fatalf("global path resolution: %+v, %v", cfg, err)
			}
		})
	}
	for _, invalid := range []string{"~another/config", "bad\x00path"} {
		cwd, env := fixture(t)
		env["GH_CONFIG_DIR"] = invalid
		if _, err := Load(cwd, lookup(env), nil); err == nil || !strings.Contains(err.Error(), "global configuration directory") {
			t.Fatalf("invalid configuration path %q: %v", invalid, err)
		}
	}
}

func TestYAMLAliasesAndBooleans(t *testing.T) {
	cwd, env := fixture(t)
	write(t, filepath.Join(cwd, ".gh-star-repos.yml"), "topic: &topics [cli, tools]\nlist: *topics\ncreate-lists: &enabled TRUE\ndry-run: *enabled\nsleep: &limit 2\nmax-repos: *limit\n")
	cfg, err := Load(cwd, lookup(env), nil)
	if err != nil || !cfg.DryRun || !cfg.CreateLists || cfg.Sleep != 2 || cfg.MaxRepos != 2 || !reflect.DeepEqual(cfg.Lists, []string{"cli", "tools"}) {
		t.Fatalf("YAML aliases or boolean case rejected: %+v, %v", cfg, err)
	}
}

func TestSleepBoundariesAndSourcedErrors(t *testing.T) {
	cwd, env := fixture(t)
	if cfg, err := Load(cwd, lookup(env), map[string]string{"sleep": "300"}); err != nil || cfg.Sleep != 300 {
		t.Fatalf("maximum sleep rejected: %+v, %v", cfg, err)
	}
	env["GHSR_SLEEP"], env["GHSR_OPT_SLEEP"] = "0", "301"
	if _, err := Load(cwd, lookup(env), nil); err == nil || !strings.Contains(err.Error(), "GHSR_OPT_SLEEP: sleep must be an integer from 0 to 300") {
		t.Fatalf("missing sourced range error: %v", err)
	}
	if cfg, err := Load(cwd, lookup(env), map[string]string{"sleep": "0"}); err != nil || cfg.Sleep != 0 {
		t.Fatalf("CLI did not override invalid environment: %+v, %v", cfg, err)
	}
	delete(env, "GHSR_OPT_SLEEP")
	env["GHSR_OPT_TOPIC"], env["GHSR_TOPIC"] = "", "alias"
	if cfg, err := Load(cwd, lookup(env), nil); err != nil || len(cfg.Topics) != 0 || cfg.Sources["topic"] != "GHSR_OPT_TOPIC" {
		t.Fatalf("empty canonical value did not clear alias: %+v, %v", cfg, err)
	}
}

func TestUnknownOverridesHaveStableErrors(t *testing.T) {
	cwd, env := fixture(t)
	for range 20 {
		_, err := Load(cwd, lookup(env), map[string]string{"z-unknown": "", "a-unknown": ""})
		if err == nil || err.Error() != `unknown command line setting "a-unknown"` {
			t.Fatalf("unstable unknown setting error: %v", err)
		}
	}
}
