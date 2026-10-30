// Package plugin has no runtime code; its tests guard the Claude Code plugin
// packaging so a version bump or file rename cannot silently break the
// marketplace install.
package plugin

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const root = "../.."

func readJSON(t *testing.T, rel string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
}

func TestVersionsInSync(t *testing.T) {
	var pj struct{ Name, Version, Icon string }
	readJSON(t, ".claude-plugin/plugin.json", &pj)
	var mj struct {
		Plugins []struct{ Name, Version, Source string }
	}
	readJSON(t, ".claude-plugin/marketplace.json", &mj)

	launcher, err := os.ReadFile(filepath.Join(root, "bin/budgeteer-run"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^VERSION="([^"]+)"`).FindSubmatch(launcher)
	if m == nil {
		t.Fatal("launcher has no VERSION line")
	}
	if len(mj.Plugins) != 1 || mj.Plugins[0].Name != pj.Name {
		t.Fatalf("marketplace must list exactly the plugin %q: %+v", pj.Name, mj.Plugins)
	}
	if got := mj.Plugins[0].Version; got != pj.Version {
		t.Errorf("marketplace version %s != plugin.json %s", got, pj.Version)
	}
	if got := string(m[1]); got != pj.Version {
		t.Errorf("launcher VERSION %s != plugin.json %s", got, pj.Version)
	}
	if _, err := os.Stat(filepath.Join(root, pj.Icon)); err != nil {
		t.Errorf("icon %q missing: %v", pj.Icon, err)
	}

	// The Codex plugin must ship the same version as the Claude one.
	var cj struct{ Name, Version string }
	readJSON(t, "plugins/budgeteer/.codex-plugin/plugin.json", &cj)
	var cm struct {
		Plugins []struct{ Name, Version, Source string }
	}
	readJSON(t, ".agents/plugins/marketplace.json", &cm)
	if cj.Version != pj.Version {
		t.Errorf("codex plugin version %s != %s", cj.Version, pj.Version)
	}
	if len(cm.Plugins) != 1 || cm.Plugins[0].Version != pj.Version || cm.Plugins[0].Source != "./plugins/budgeteer" {
		t.Errorf("codex marketplace entry out of sync: %+v", cm.Plugins)
	}
	if _, err := os.Stat(filepath.Join(root, "plugins/budgeteer/skills/budgeteer/SKILL.md")); err != nil {
		t.Errorf("codex skill missing: %v", err)
	}
}

func TestLauncherIsExecutableAndValidShell(t *testing.T) {
	p := filepath.Join(root, "bin/budgeteer-run")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&0o111 == 0 {
		t.Error("launcher is not executable; plugin installs would fail with 'permission denied'")
	}
	if out, err := exec.Command("sh", "-n", p).CombinedOutput(); err != nil {
		t.Errorf("sh -n: %v\n%s", err, out)
	}
}

func TestCommandsAndSkillHaveFrontmatter(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "commands/*.md"))
	files = append(files, filepath.Join(root, "skills/budgeteer/SKILL.md"), filepath.Join(root, "plugins/budgeteer/skills/budgeteer/SKILL.md"), filepath.Join(root, "integrations/codex/budgeteer.md"))
	if len(files) < 5 {
		t.Fatalf("expected commands, skill and codex prompt, found %v", files)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.HasPrefix(s, "---\n") || !strings.Contains(s[4:], "\n---\n") || !strings.Contains(s, "description:") {
			t.Errorf("%s: missing frontmatter with a description", f)
		}
	}
}
