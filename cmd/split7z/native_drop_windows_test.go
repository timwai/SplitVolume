//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeDropUsesSourcePaths(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "中文 原始源文件.tar.gz")
	if err := os.WriteFile(source, []byte("original-content"), 0600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "源文件夹")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	app := newApp()
	if err := app.add([]string{source, directory, source}); err != nil {
		t.Fatal(err)
	}
	state := app.state()
	if len(state.Inputs) != 2 {
		t.Fatalf("got %d items; want deduplicated original file and folder", len(state.Inputs))
	}
	if state.Inputs[0].Path != source || state.Inputs[1].Path != directory {
		t.Fatalf("paths were modified: %+v", state.Inputs)
	}
	if state.OutputDir != root {
		t.Fatalf("incorrect output directory %q", state.OutputDir)
	}
	if err := app.add([]string{filepath.Join(root, "missing")}); err == nil {
		t.Fatal("missing paths must not be accepted as a successful drop")
	}
}
