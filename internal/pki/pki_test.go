package pki

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateExclusiveAndPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	if err := Generate(dir, "localhost"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Generate(dir, "localhost"); err == nil {
		t.Fatal("overwrote existing PKI")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "server.key"))
	if string(before) != string(after) {
		t.Fatal("key changed")
	}
	mode, _ := os.Stat(filepath.Join(dir, "server.key"))
	if mode.Mode().Perm()&0077 != 0 {
		t.Fatal("key readable by other users")
	}
}
