package archiver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Config, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "资料 Folder")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "说明.txt"), []byte("hello 你好"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Inputs: []string{source}, OutputDir: filepath.Join(root, "输出"), ArchiveName: "测试文档", VolumeMiB: 2, Level: 5, SevenZip: "mock-7zr", Format: "7z"}
	return cfg, root
}

func TestValidation(t *testing.T) {
	cfg, _ := fixture(t)
	if _, err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.OutputDir = ""
	if _, err := Validate(cfg); err == nil {
		t.Fatal("empty output dir must be rejected")
	}
	cfg, _ = fixture(t)
	cfg.OutputDir = filepath.Join(cfg.Inputs[0], "output")
	if _, err := Validate(cfg); err == nil {
		t.Fatal("output inside source folder must be rejected")
	}
	cfg, _ = fixture(t)
	cfg.VolumeMiB = 0
	if _, err := Validate(cfg); err == nil {
		t.Fatal("zero volume size must be rejected")
	}
	cfg, _ = fixture(t)
	cfg.ArchiveName = "CON.txt"
	if _, err := Validate(cfg); err == nil {
		t.Fatal("device filename must be rejected")
	}
	cfg, _ = fixture(t)
	cfg.ArchiveName = "../bad"
	if _, err := Validate(cfg); err == nil {
		t.Fatal("path-like filename must be rejected")
	}
	cfg, _ = fixture(t)
	cfg.Inputs = append(cfg.Inputs, filepath.Join(cfg.Inputs[0], "说明.txt"))
	cleaned, err := CleanInputs(cfg.Inputs)
	if err != nil || len(cleaned) != 1 {
		t.Fatalf("nested input dedupe failed: %v, %#v", err, cleaned)
	}
}

func TestNoOverwrite(t *testing.T) {
	cfg, _ := fixture(t)
	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.OutputDir, cfg.ArchiveName+".7z.001"), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Validate(cfg)
	if err == nil {
		t.Fatal("did not protect existing volume")
	}
}

func TestParser(t *testing.T) {
	var seen []int
	p := &streamParser{phase: "compress", lastPercent: -1, emit: func(v Progress) {
		if v.Message == "" {
			seen = append(seen, v.Percent)
		}
	}}
	_, _ = p.Write([]byte("  1% A\r 27% B\r"))
	_, _ = p.Write([]byte("100% C\n"))
	p.flush()
	if len(seen) != 3 || seen[0] != 1 || seen[1] != 27 || seen[2] != 100 {
		t.Fatalf("percent parser wrong: %#v", seen)
	}
}

// This is an integration test for argument handling, incremental output,
// archive discovery, and integrity verification using a fake 7-Zip command.
// A separate smoke test on Windows with the real 7zr.exe is still necessary.
func TestRunWithMock7Zip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell mock")
	}
	cfg, root := fixture(t)
	mock := filepath.Join(root, "mock7z.sh")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" >> "` + filepath.Join(root, "args.txt") + `"
if [ "$1" = "a" ]; then
  printf ' 31%%\r 84%%\r 100%%\n'
  for arg in "$@"; do
    case "$arg" in
      -v2097152b) : ;;
      -v*b) exit 20 ;;
    esac
    case "$arg" in
      *.7z) printf 'test' > "$arg.001"; printf 'test2' > "$arg.002" ;;
    esac
  done
elif [ "$1" = "t" ]; then
  printf '100%%\nEverything is Ok\n'
else
  exit 2
fi
`
	if err := os.WriteFile(mock, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	cfg.SevenZip = mock
	cfg.Verify = true
	var updates []Progress
	result, err := Run(context.Background(), cfg, func(p Progress) { updates = append(updates, p) })
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 2 || result.TotalSize != 9 {
		t.Fatalf("bad output: %#v", result)
	}
	cmdline, _ := os.ReadFile(filepath.Join(root, "args.txt"))
	if !strings.Contains(string(cmdline), "-scsUTF-8") || !strings.Contains(string(cmdline), "-v2097152b") {
		t.Fatalf("missing Unicode/volume switches: %s", cmdline)
	}
	if !strings.Contains(string(cmdline), "@") {
		t.Fatalf("missing 7zip listfile: %s", cmdline)
	}
	if len(updates) < 4 || updates[len(updates)-1].Phase != "done" {
		t.Fatalf("unexpected progress: %#v", updates)
	}
	// Second attempt must refuse to overwrite.
	_, err = Run(context.Background(), cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("should refuse overwrite: %v", err)
	}
}

func TestCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell mock")
	}
	cfg, root := fixture(t)
	mock := filepath.Join(root, "cancel.sh")
	contents := "#!/bin/sh\nfor arg in \"$@\"; do case \"$arg\" in *.7z) echo 'partial' > \"$arg.001\";; esac; done\nsleep 30\n"
	if err := os.WriteFile(mock, []byte(contents), 0755); err != nil {
		t.Fatal(err)
	}
	cfg.SevenZip = mock
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := Run(ctx, cfg, func(p Progress) {
		if p.Phase == "compress" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
	parts, _, err := archiveParts(filepath.Join(cfg.OutputDir, cfg.ArchiveName+".7z"))
	if err != nil || len(parts) != 0 {
		t.Fatalf("partial archive not cleaned: %#v %v", parts, err)
	}
}
