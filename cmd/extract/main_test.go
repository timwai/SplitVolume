package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArchive(t *testing.T, folder, name string, data string) (string, string) {
	t.Helper()
	gzFile := filepath.Join(folder, "pack.tar.gz")
	f, e := os.Create(gzFile)
	if e != nil {
		t.Fatal(e)
	}
	gz := gzip.NewWriter(f)
	tr := tar.NewWriter(gz)
	if e = tr.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(data))}); e != nil {
		t.Fatal(e)
	}
	if _, e = io.WriteString(tr, data); e != nil {
		t.Fatal(e)
	}
	if e = tr.Close(); e != nil {
		t.Fatal(e)
	}
	if e = gz.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(gzFile)
	if e != nil {
		t.Fatal(e)
	}
	split := len(b) / 2
	first := gzFile + ".001"
	if e = os.WriteFile(first, b[:split], 0644); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(gzFile+".002", b[split:], 0644); e != nil {
		t.Fatal(e)
	}
	return first, gzFile
}
func TestExtractRoundtrip(t *testing.T) {
	dir := t.TempDir()
	first, _ := writeArchive(t, dir, "中文 文件/资料.txt", "hello Linux")
	out := filepath.Join(dir, "out")
	if e := extract(first, out); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(out, "中文 文件", "资料.txt"))
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != "hello Linux" {
		t.Fatal("content mismatch")
	}
	if e = extract(first, out); e == nil {
		t.Fatal("must not overwrite")
	}
}
func TestRejectTraversal(t *testing.T) {
	dir := t.TempDir()
	first, _ := writeArchive(t, dir, "../../escape.txt", "pwn")
	if e := extract(first, filepath.Join(dir, "out")); e == nil || !strings.Contains(e.Error(), "不安全") {
		t.Fatalf("expected traversal blocked, got %v", e)
	}
}
func TestMissingMiddlePart(t *testing.T) {
	dir := t.TempDir()
	first, base := writeArchive(t, dir, "file.txt", "ok")
	if e := os.Rename(base+".002", base+".003"); e != nil {
		t.Fatal(e)
	}
	if e := extract(first, filepath.Join(dir, "out")); e == nil || !strings.Contains(e.Error(), "缺少") {
		t.Fatalf("expected missing part blocked, got %v", e)
	}
}
