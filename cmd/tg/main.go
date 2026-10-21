package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/DashSaman/tg/internal/config"
	"github.com/DashSaman/tg/internal/engine"
	"github.com/DashSaman/tg/internal/pki"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

var version = "0.1.0-dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := command(ctx, os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "tg:", err)
		os.Exit(1)
	}
}
func command(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: tg init --dir DIR [--server-name NAME] | check --config FILE | run --config FILE | version")
	}
	if args[0] == "version" && len(args) == 1 {
		_, err := fmt.Fprintln(out, "tg", version)
		return err
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(out)
	switch args[0] {
	case "init":
		dir := flags.String("dir", "", "new private directory for configs and test identities")
		name := flags.String("server-name", "localhost", "verified server DNS name or IP")
		redundant := flags.Bool("redundant", false, "enable acknowledged packet redundancy across reliable carriers; extra bandwidth required")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *dir == "" || flags.NArg() != 0 {
			return errors.New("init requires --dir and no positional arguments")
		}
		return initialize(*dir, *name, *redundant, out)
	case "check", "run":
		path := flags.String("config", "", "JSON configuration path")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *path == "" || flags.NArg() != 0 {
			return errors.New("--config required; no positional arguments")
		}
		c, err := config.Load(*path)
		if err != nil {
			return err
		}
		if err = pki.Check(c); err != nil {
			return err
		}
		if args[0] == "check" {
			_, err = fmt.Fprintln(out, "configuration and local credentials valid")
			return err
		}
		e, err := engine.New(c)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "tg %s: starting %s; carrier health is not end-to-end application health\n", version, c.Role)
		return e.Run(ctx)
	default:
		return errors.New("unknown command")
	}
}
func initialize(dir, name string, redundant bool, out io.Writer) error {
	sessionID := make([]byte, 16)
	path := make([]byte, 16)
	if _, err := rand.Read(sessionID); err != nil {
		return err
	}
	if _, err := rand.Read(path); err != nil {
		return err
	}
	if err := pki.Generate(dir, name); err != nil {
		return err
	}
	c := config.Defaults()
	c.Redundant = redundant
	c.SessionID = hex.EncodeToString(sessionID)
	c.TLS.ServerName = name
	c.Carriers[0].Path = "/" + hex.EncodeToString(path)
	for _, role := range []string{"edge", "exit"} {
		c.Role = role
		if role == "exit" {
			c.AppAddress = "127.0.0.1:51820"
			c.Metrics = "127.0.0.1:19091"
			c.TLS.Cert = "server.crt"
			c.TLS.Key = "server.key"
		}
		b, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(dir, role+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(append(b, '\n'))
		ce := f.Close()
		if err != nil {
			return err
		}
		if ce != nil {
			return ce
		}
	}
	_, err := fmt.Fprintln(out, "created private lab pair; keep ca.key offline; provision each host only its own identity, config, CA certificate and web token")
	return err
}
