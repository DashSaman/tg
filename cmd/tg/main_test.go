package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInitCreatesCheckablePairAndNeverOverwrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lab")
	var out bytes.Buffer
	if err := command(context.Background(), []string{"init", "--dir", dir, "--server-name", "localhost"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"edge.json", "exit.json"} {
		if err := command(context.Background(), []string{"check", "--config", filepath.Join(dir, name)}, &out); err != nil {
			t.Fatal(name, err)
		}
	}
	before, err := os.ReadFile(filepath.Join(dir, "web.token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := command(context.Background(), []string{"init", "--dir", dir}, &out); err == nil {
		t.Fatal("overwrote existing identities")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "web.token"))
	if !bytes.Equal(before, after) {
		t.Fatal("secret modified")
	}
	if bytes.Contains(out.Bytes(), bytes.TrimSpace(before)) {
		t.Fatal("printed secret")
	}
	if err := os.Remove(filepath.Join(dir, "edge.key")); err != nil {
		t.Fatal(err)
	}
	if err := command(context.Background(), []string{"check", "--config", filepath.Join(dir, "edge.json")}, &out); err == nil {
		t.Fatal("missing TLS identity accepted")
	}
}

func TestCLIRejectsUnknownAndExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"oops"}, {"init", "--dir", "x", "unexpected"}, {"check"}, {"run", "--config", "missing"}} {
		if err := command(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Fatal(args)
		}
	}
}
