package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mountinfoWithRunOverlay is one line of /proc/self/mountinfo while the overlay mount on
// /run holds.
const mountinfoWithRunOverlay = `4102 3693 0:81 / /run rw,relatime - overlay overlay rw,lowerdir=/run,upperdir=/var/lib/hydrascale/state/jbones/run-scratch/upper,workdir=/var/lib/hydrascale/state/jbones/run-scratch/work
`

func testExecOptions(t *testing.T) (nsExecOptions, *[]string, *[]string) {
	t.Helper()
	var started []string
	var linked []string
	opts := nsExecOptions{
		socket:   "/var/lib/hydrascale/state/jbones/tailscaled.sock",
		scratch:  "/var/lib/hydrascale/state/jbones/run-scratch",
		mountRun: func(scratch string) error { return nil },
		linkSocket: func(socket string) error {
			linked = append(linked, socket)
			return nil
		},
		execChild: func(args []string) error {
			started = args
			return nil
		},
	}
	return opts, &started, &linked
}

func TestNsExec_links_the_socket_then_starts_the_child(t *testing.T) {
	opts, started, linked := testExecOptions(t)

	args := []string{"tailscale", "status"}
	if err := runNsExec(opts, args); err != nil {
		t.Fatalf("runNsExec: %v", err)
	}
	if len(*linked) != 1 || (*linked)[0] != opts.socket {
		t.Errorf("linked = %v, want one entry %q", *linked, opts.socket)
	}
	if len(*started) != 2 || (*started)[0] != "tailscale" {
		t.Errorf("the child started with %v, want %v", *started, args)
	}
}

func TestLinkSocketAt_replaces_the_socket_of_the_host(t *testing.T) {
	// A host that runs the stock tailscaled holds a socket at this path, and the lower
	// layer of the overlay mount shows it. The link must still hold.
	dir := filepath.Join(t.TempDir(), "tailscale")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "tailscaled.sock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	socket := "/var/lib/hydrascale/state/jbones/tailscaled.sock"
	if err := linkSocketAt(dir, path, socket); err != nil {
		t.Fatalf("linkSocketAt: %v", err)
	}
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != socket {
		t.Errorf("the link names %q, want %q", target, socket)
	}
}

func TestLinkSocketAt_creates_the_directory_and_the_link(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tailscale")
	path := filepath.Join(dir, "tailscaled.sock")
	socket := "/var/lib/hydrascale/state/havoc/tailscaled.sock"

	if err := linkSocketAt(dir, path, socket); err != nil {
		t.Fatalf("linkSocketAt: %v", err)
	}
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != socket {
		t.Errorf("the link names %q, want %q", target, socket)
	}
}

func TestLinkSocketAt_reports_a_directory_it_cannot_create(t *testing.T) {
	// A file cannot hold a directory, so the creation fails.
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dir := filepath.Join(blocked, "tailscale")

	err := linkSocketAt(dir, filepath.Join(dir, "tailscaled.sock"), "/var/lib/hydrascale/state/jbones/tailscaled.sock")
	if err == nil {
		t.Fatal("linkSocketAt returned no error, want the directory failure")
	}
}

func TestNsExec_exits_non_zero_when_the_overlay_mount_fails(t *testing.T) {
	opts, started, linked := testExecOptions(t)
	opts.mountRun = func(scratch string) error { return errors.New("invalid argument") }

	err := runNsExec(opts, []string{"tailscale", "status"})
	if err == nil {
		t.Fatal("runNsExec returned no error, want a non-nil error so the child exits non-zero")
	}
	if !strings.Contains(err.Error(), "invalid argument") {
		t.Errorf("error = %q, want it to hold the mount error text", err)
	}
	if !strings.Contains(err.Error(), "--socket") {
		t.Errorf("error = %q, want it to name the --socket option", err)
	}
	if *started != nil {
		t.Errorf("the child started with %v, want no child", *started)
	}
	if *linked != nil {
		t.Errorf("linked = %v, want no link when the mount fails", *linked)
	}
}

func TestNsExec_exits_non_zero_when_the_link_fails(t *testing.T) {
	opts, started, _ := testExecOptions(t)
	opts.linkSocket = func(socket string) error { return errors.New("file exists") }

	err := runNsExec(opts, []string{"tailscale", "status"})
	if err == nil {
		t.Fatal("runNsExec returned no error, want the link failure")
	}
	if *started != nil {
		t.Errorf("the child started with %v, want no child", *started)
	}
}

func TestNsExec_starts_the_child_when_no_socket_is_given(t *testing.T) {
	opts, started, linked := testExecOptions(t)
	opts.socket = ""
	opts.mountRun = func(scratch string) error {
		t.Error("mountRun ran, want no mount without a socket")
		return nil
	}

	if err := runNsExec(opts, []string{"ip", "addr"}); err != nil {
		t.Fatalf("runNsExec: %v", err)
	}
	if len(*started) != 2 {
		t.Errorf("the child started with %v, want two arguments", *started)
	}
	if *linked != nil {
		t.Errorf("linked = %v, want no link without a socket", *linked)
	}
}

func TestNsExec_rejects_an_empty_command(t *testing.T) {
	opts, _, _ := testExecOptions(t)

	if err := runNsExec(opts, nil); err == nil {
		t.Fatal("runNsExec returned no error, want a rejection of the empty command")
	}
}

func TestHasOverlayOn_finds_an_overlay_mount_on_run(t *testing.T) {
	if !hasOverlayOn(strings.NewReader(mountinfoWithRunOverlay), "/run") {
		t.Error("hasOverlayOn = false, want true")
	}
}

func TestHasOverlayOn_rejects_an_overlay_mount_on_another_mount_point(t *testing.T) {
	if hasOverlayOn(strings.NewReader(mountinfoWithRunOverlay), "/etc") {
		t.Error("hasOverlayOn = true, want false")
	}
}
