package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/jakelazaroff/unpm/internal/cfg"
)

func stubApp() *App {
	return &App{
		Vendor: func(c *cfg.Config) ([]string, error) { return nil, nil },
		Check:  func(c *cfg.Config) error { return nil },
		Why:    func(c *cfg.Config, file string) error { return nil },
	}
}

const testConfig = "testdata/unpm.json"

func TestHelpNoArgs(t *testing.T) {
	var stderr bytes.Buffer
	code := stubApp().Run([]string{"unpm"}, nil, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("usage:")) {
		t.Fatalf("expected help text, got %q", stderr.String())
	}
}

func TestHelpCommand(t *testing.T) {
	var stderr bytes.Buffer
	code := stubApp().Run([]string{"unpm", "help"}, nil, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("usage:")) {
		t.Fatalf("expected help text, got %q", stderr.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var stderr bytes.Buffer
	code := stubApp().Run([]string{"unpm", "bogus"}, nil, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("unknown command")) {
		t.Fatalf("expected unknown command error, got %q", stderr.String())
	}
}

func TestBadConfig(t *testing.T) {
	var stderr bytes.Buffer
	code := stubApp().Run([]string{"unpm", "vendor", "--config", "nonexistent.json"}, nil, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("error:")) {
		t.Fatalf("expected error message, got %q", stderr.String())
	}
}

func TestVendorSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := stubApp().Run([]string{"unpm", "vendor", "--config", testConfig, "--verbose"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("done.")) {
		t.Fatalf("expected done message, got %q", stdout.String())
	}
}

func TestVendorWarnings(t *testing.T) {
	app := stubApp()
	app.Vendor = func(c *cfg.Config) ([]string, error) {
		return []string{"something looks off"}, nil
	}

	var stdout, stderr bytes.Buffer
	code := app.Run([]string{"unpm", "vendor", "--config", testConfig, "--verbose"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("something looks off")) {
		t.Fatalf("expected warning in stderr, got %q", stderr.String())
	}
}

func TestVendorError(t *testing.T) {
	app := stubApp()
	app.Vendor = func(c *cfg.Config) ([]string, error) {
		return nil, errors.New("download failed")
	}

	var stdout, stderr bytes.Buffer
	code := app.Run([]string{"unpm", "vendor", "--config", testConfig, "--verbose"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("download failed")) {
		t.Fatalf("expected error in stderr, got %q", stderr.String())
	}
}

func TestCheckSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := stubApp().Run([]string{"unpm", "check", "--config", testConfig}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("done.")) {
		t.Fatalf("expected done message, got %q", stdout.String())
	}
}

func TestCheckError(t *testing.T) {
	app := stubApp()
	app.Check = func(c *cfg.Config) error {
		return errors.New("bad import map")
	}

	var stderr bytes.Buffer
	code := app.Run([]string{"unpm", "check", "--config", testConfig}, nil, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("bad import map")) {
		t.Fatalf("expected error in stderr, got %q", stderr.String())
	}
}

func TestWhySuccess(t *testing.T) {
	var stderr bytes.Buffer
	code := stubApp().Run([]string{"unpm", "why", "--config", testConfig, "some/file.js"}, nil, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}
}

func TestWhyMissingArg(t *testing.T) {
	var stderr bytes.Buffer
	code := stubApp().Run([]string{"unpm", "why", "--config", testConfig}, nil, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("usage: unpm why")) {
		t.Fatalf("expected usage message, got %q", stderr.String())
	}
}

func TestWhyError(t *testing.T) {
	app := stubApp()
	app.Why = func(c *cfg.Config, file string) error {
		return errors.New("not found")
	}

	var stderr bytes.Buffer
	code := app.Run([]string{"unpm", "why", "--config", testConfig, "some/file.js"}, nil, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("not found")) {
		t.Fatalf("expected error in stderr, got %q", stderr.String())
	}
}

func TestFlagsAfterPositionalArgs(t *testing.T) {
	var called bool
	app := stubApp()
	app.Why = func(c *cfg.Config, file string) error {
		called = true
		if file != "some/file.js" {
			t.Fatalf("expected file arg %q, got %q", "some/file.js", file)
		}
		return nil
	}

	var stderr bytes.Buffer
	code := app.Run([]string{"unpm", "why", "some/file.js", "--config", testConfig}, nil, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}
	if !called {
		t.Fatal("Why was not called")
	}
}

func TestFlagOverridesConfig(t *testing.T) {
	app := stubApp()
	app.Vendor = func(c *cfg.Config) ([]string, error) {
		if c.Unpm.Out != "custom-out" {
			t.Fatalf("expected out %q, got %q", "custom-out", c.Unpm.Out)
		}
		if c.Unpm.Root != "/custom-root" {
			t.Fatalf("expected root %q, got %q", "/custom-root", c.Unpm.Root)
		}
		return nil, nil
	}

	var stdout, stderr bytes.Buffer
	code := app.Run([]string{"unpm", "vendor", "--config", testConfig, "--out", "custom-out", "--root", "/custom-root", "--verbose"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}
}
