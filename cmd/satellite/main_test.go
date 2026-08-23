package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveWakeAssetsFromExplicitPath(t *testing.T) {
	directory := t.TempDir()
	library := "libonnxruntime.so"
	if runtime.GOOS == "darwin" {
		library = "libonnxruntime.dylib"
	} else if runtime.GOOS == "windows" {
		library = "onnxruntime.dll"
	}
	runtimeDirectory := filepath.Join(directory, "runtime")
	if err := os.Mkdir(runtimeDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDirectory, library), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveWakeAssets("openwakeword", directory)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != directory {
		t.Fatalf("expected %q, got %q", directory, resolved)
	}
}

func TestResolveMicroWakeAssetsFromExplicitPath(t *testing.T) {
	directory := t.TempDir()
	python := filepath.Join(directory, "venv", "bin", "python")
	if runtime.GOOS == "windows" {
		python = filepath.Join(directory, "venv", "Scripts", "python.exe")
	}
	if err := os.MkdirAll(filepath.Dir(python), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(directory, "Kuza.json"), python} {
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := resolveWakeAssets("micro", directory)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != directory {
		t.Fatalf("expected %q, got %q", directory, resolved)
	}
}
