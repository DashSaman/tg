package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type workerPaths struct {
	Edge string `json:"edge_config"`
	Exit string `json:"exit_config"`
}

// Each worker owns a separate session, PKI, sender log and application socket.
// Flow assignment belongs before WireGuard encryption, never to a ciphertext
// packet round-robin that would break application peers or per-session replay.
func initializeWorkers(dir, name string, redundant bool, count int, out io.Writer) error {
	if count < 1 || count > 32 || name == "" {
		return errors.New("init requires 1..32 instances and a server name")
	}
	if count == 1 {
		return initialize(dir, name, redundant, out)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return errors.New("worker output directory must not exist")
	}
	manifest := struct {
		Version          int           `json:"version"`
		CapacityVerified bool          `json:"capacity_verified"`
		Assignment       string        `json:"assignment"`
		Workers          []workerPaths `json:"workers"`
	}{Version: 1, Assignment: "assign connections or IP subnets to independent WireGuard peers before encryption; capacity requires measurement"}
	for i := 0; i < count; i++ {
		worker := fmt.Sprintf("worker-%02d", i+1)
		if err := initializeWorker(filepath.Join(dir, worker), name, redundant, i, io.Discard); err != nil {
			// Preserve partial private output for diagnosis. Never delete existing
			// credentials, recursively clean an operator path, or retry over it.
			return fmt.Errorf("initialize %s: %w", worker, err)
		}
		manifest.Workers = append(manifest.Workers, workerPaths{worker + "/edge.json", worker + "/exit.json"})
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "workers.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	_, err = fmt.Fprintf(out, "created %d independent private pairs; provision each host only its own credentials; assign flows/subnets separately; aggregate capacity unverified\n", count)
	return err
}
