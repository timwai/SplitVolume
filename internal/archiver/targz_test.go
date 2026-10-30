package archiver

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTarFixture(t *testing.T) (Config, []byte) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "测试 文件夹")
	if e := os.MkdirAll(filepath.Join(dir, "空目录"), 0755); e != nil {
		t.Fatal(e)
	}
	content := make([]byte, 3*1024*1024+100)
	if _, e := rand.Read(content); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "大型数据.bin"), content, 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "中文 文档.txt"), []byte("hello 中文\n"), 0644); e != nil {
		t.Fatal(e)
	}
	return Config{Inputs: []string{dir}, OutputDir: filepath.Join(root, "output"), ArchiveName: "多个分卷", Format: "targz", VolumeMiB: 1, Level: 5, Verify: true}, content
}

func TestTarGzRealRoundTrip(t *testing.T) {
	cfg, original := makeTarFixture(t)
	var ps []Progress
	result, e := Run(context.Background(), cfg, func(p Progress) { ps = append(ps, p) })
	if e != nil {
		t.Fatal(e)
	}
	if result.OriginalSize != int64(len(original)+len([]byte("hello 中文\n"))) {
		t.Fatalf("original bytes mismatch: %d", result.OriginalSize)
	}
	if len(result.Files) < 3 {
		t.Fatalf("expected several volumes, got %v", result.Files)
	}
	var concatenated bytes.Buffer
	for i, part := range result.Files {
		if !strings.HasSuffix(part, "."+formatPart(i+1)) {
			t.Fatalf("bad volume suffix: %s", part)
		}
		f, e := os.Open(part)
		if e != nil {
			t.Fatal(e)
		}
		n, e := io.Copy(&concatenated, f)
		_ = f.Close()
		if e != nil {
			t.Fatal(e)
		}
		if n > 1048576 {
			t.Fatalf("oversized part %s %d", part, n)
		}
	}
	if e := StreamArchive(context.Background(), bytes.NewReader(concatenated.Bytes()), nil); e != nil {
		t.Fatal(e)
	}
	gz, e := gzip.NewReader(bytes.NewReader(concatenated.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var got []byte
	foundEmpty := false
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if strings.HasSuffix(h.Name, "大型数据.bin") {
			got, e = io.ReadAll(tr)
			if e != nil {
				t.Fatal(e)
			}
		}
		if strings.HasSuffix(h.Name, "空目录/") {
			foundEmpty = true
		}
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("recovered content differs: expected %d got %d", len(original), len(got))
	}
	if !foundEmpty {
		t.Fatal("empty directory not preserved")
	}
	if ps[len(ps)-1].Phase != "done" {
		t.Fatal("no final done event")
	}
	if _, e := Run(context.Background(), cfg, nil); e == nil {
		t.Fatal("should not overwrite")
	}
}
func formatPart(i int) string { return fmt.Sprintf("%03d", i) }

func TestTarGzCancellationRemovesPartial(t *testing.T) {
	cfg, _ := makeTarFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, e := Run(ctx, cfg, func(p Progress) {
		if p.Phase == "compress" && p.Percent >= 5 {
			cancel()
		}
	})
	if e == nil {
		t.Fatal("expected cancellation")
	}
	parts, _ := filepath.Glob(filepath.Join(cfg.OutputDir, "*.tar.gz.*"))
	if len(parts) != 0 {
		t.Fatalf("partial volumes remain: %v", parts)
	}
}

func TestTarGzCorruptionDetected(t *testing.T) {
	cfg, _ := makeTarFixture(t)
	cfg.Verify = false
	result, e := Run(context.Background(), cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	all := []byte{}
	for _, part := range result.Files {
		b, e := os.ReadFile(part)
		if e != nil {
			t.Fatal(e)
		}
		all = append(all, b...)
	}
	all[len(all)-6] ^= 0xff
	if e := StreamArchive(context.Background(), bytes.NewReader(all), nil); e == nil {
		t.Fatal("CRC corruption must be detected")
	}
}
