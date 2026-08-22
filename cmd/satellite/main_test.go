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
	resolved, err := resolveWakeAssets(directory)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != directory {
		t.Fatalf("expected %q, got %q", directory, resolved)
	}
}
