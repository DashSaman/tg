package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/DashSaman/tg/internal/config"
)

func TestInitIndependentWorkersUseUniquePortsAndIdentities(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fleet")
	var out bytes.Buffer
	if err := command(context.Background(), []string{"init", "--dir", dir, "--server-name", "localhost", "--redundant", "--instances", "3"}, &out); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	unique := func(value string) {
		t.Helper()
		if seen[value] {
			t.Fatal("workers share an endpoint or identity")
		}
		seen[value] = true
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "workers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		CapacityVerified bool `json:"capacity_verified"`
		Workers          []struct {
			Edge string `json:"edge_config"`
			Exit string `json:"exit_config"`
		} `json:"workers"`
	}
	if err := json.Unmarshal(manifest, &listing); err != nil || len(listing.Workers) != 3 || listing.CapacityVerified {
		t.Fatal("invalid or misleading fleet manifest", err)
	}
	for i, worker := range listing.Workers {
		name := fmt.Sprintf("worker-%02d", i+1)
		if worker.Edge != name+"/edge.json" || worker.Exit != name+"/exit.json" {
			t.Fatal("manifest paths", worker)
		}
		token, err := os.ReadFile(filepath.Join(dir, name, "web.token"))
		if err != nil {
			t.Fatal(err)
		}
		unique("token:" + string(token))
		if bytes.Contains(out.Bytes(), bytes.TrimSpace(token)) || bytes.Contains(manifest, bytes.TrimSpace(token)) {
			t.Fatal("secret disclosed")
		}
		var session string
		for _, role := range []string{"edge", "exit"} {
			path := filepath.Join(dir, name, role+".json")
			c, err := config.Load(path)
			if err != nil || !c.Redundant {
				t.Fatal("worker configuration", err)
			}
			if err := command(context.Background(), []string{"check", "--config", path}, &out); err != nil {
				t.Fatal(err)
			}
			unique("app:" + c.AppAddress)
			unique("metrics:" + c.Metrics)
			if role == "edge" {
				session = c.SessionID
				unique("session:" + session)
				for _, carrier := range c.Carriers {
					unique("carrier:" + carrier.Kind + ":" + carrier.Address)
				}
			} else if c.SessionID != session {
				t.Fatal("pair session mismatch")
			}
		}
	}
	before, err := os.ReadFile(filepath.Join(dir, "worker-01", "web.token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := command(context.Background(), []string{"init", "--dir", dir, "--instances", "3"}, &out); err == nil {
		t.Fatal("overwrote fleet")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "worker-01", "web.token"))
	if !bytes.Equal(before, after) {
		t.Fatal("existing secret modified")
	}
}

func TestInitWorkerBoundsRejectBeforeCreatingDirectory(t *testing.T) {
	for _, n := range []int{0, -1, 33} {
		dir := filepath.Join(t.TempDir(), "fleet")
		if err := command(context.Background(), []string{"init", "--dir", dir, "--instances", strconv.Itoa(n)}, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid count", n)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("mutated output for invalid count")
		}
	}
}

func TestInitOneInstancePreservesFlatLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pair")
	if err := command(context.Background(), []string{"init", "--dir", dir, "--instances", "1"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "edge.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "workers.json")); !os.IsNotExist(err) {
		t.Fatal("single instance layout changed")
	}
}
