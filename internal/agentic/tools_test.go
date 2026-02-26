package agentic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolListDir(t *testing.T) {
	tmp, err := os.MkdirTemp("", "agentic_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	os.WriteFile(filepath.Join(tmp, "test.txt"), []byte("hello"), 0644)
	os.Mkdir(filepath.Join(tmp, "subdir"), 0755)

	args := map[string]any{"dirpath": tmp}
	b, _ := json.Marshal(args)
	res := ExecuteTool("list_dir", string(b))

	if !strings.Contains(res, "[file] test.txt") {
		t.Errorf("expected to find test.txt, got: %s", res)
	}
	if !strings.Contains(res, "[dir ] subdir") {
		t.Errorf("expected to find subdir, got: %s", res)
	}
}

func TestToolGrepSearch(t *testing.T) {
	tmp, err := os.MkdirTemp("", "agentic_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	os.WriteFile(filepath.Join(tmp, "test1.txt"), []byte("func toolListDir() {\n// hello grep\n}"), 0644)
	os.WriteFile(filepath.Join(tmp, "test2.txt"), []byte("func toolGrepSearch() {\n// world grep\n}"), 0644)

	args := map[string]any{"dirpath": tmp, "pattern": "hello grep"}
	b, _ := json.Marshal(args)
	res := ExecuteTool("grep_search", string(b))

	if !strings.Contains(res, "test1.txt") {
		t.Errorf("expected to find test1.txt, got: %s", res)
	}
	if strings.Contains(res, "test2.txt") {
		t.Errorf("expected NOT to find test2.txt, got: %s", res)
	}
}
