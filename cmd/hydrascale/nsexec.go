package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
)

// runMountPoint is the directory that holds the socket of tailscaled on a Linux host.
const runMountPoint = "/run"

// tailscaleSocketDir and tailscaleSocketPath are the location at which the tailscale
// command looks for the socket of tailscaled. The tailscale binary holds this path and no
// environment variable replaces it. The socket of a tailnet must therefore appear here,
// because a command without the --socket option reads no other path.
// /var/run is a symbolic link to /run, so the path in the binary resolves to
// tailscaleSocketPath.
const (
	tailscaleSocketDir  = "/run/tailscale"
	tailscaleSocketPath = "/run/tailscale/tailscaled.sock"
)

// nsExecCmd is an internal helper, not part of the user-facing CLI. The caller invokes it
// as `ip netns exec <ns> hydrascale __nsexec --socket S --scratch D -- <cmd...>`.
// The helper runs inside the private mount namespace that `ip netns exec` sets up. It
// places the socket of the tailnet where the tailscale command expects it, then it
// executes the child. The host holds one /run, so the helper places an overlay mount on
// /run rather than create /run/tailscale on the host. A private tmpfs holds the upper
// directory of the overlay mount, so two commands of one tailnet share no work directory.
func nsExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "__nsexec",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := nsExecOptions{
				mountRun:   mountRunOverlay,
				linkSocket: linkTailscaleSocket,
				execChild:  execChild,
			}
			opts.socket, _ = cmd.Flags().GetString("socket")
			opts.scratch, _ = cmd.Flags().GetString("scratch")
			dash := cmd.ArgsLenAtDash()
			if dash < 0 || dash >= len(args) {
				return fmt.Errorf("__nsexec requires '-- <command...>'")
			}
			return runNsExec(opts, args[dash:])
		},
	}
	cmd.Flags().String("socket", "", "socket of tailscaled for this tailnet")
	cmd.Flags().String("scratch", "", "directory for the private tmpfs that backs the overlay mount on /run")
	return cmd
}

// nsExecOptions holds the inputs of runNsExec.
type nsExecOptions struct {
	socket  string
	scratch string
	// mountRun places the private tmpfs and the overlay mount on /run. A test replaces it.
	mountRun func(scratch string) error
	// linkSocket places the socket of the tailnet at tailscaleSocketPath. A test replaces it.
	linkSocket func(socket string) error
	// execChild replaces the process image with the child. A test replaces it.
	execChild func(args []string) error
}

// runNsExec places the socket of the tailnet where the tailscale command expects it, then
// it executes the child.
// When a mount fails, runNsExec returns an error and starts no child. A child that runs
// without the socket reports that tailscaled is not running, which hides the true cause.
// The error names the socket, which reaches the daemon without the mount.
// When the caller gives no socket and no scratch directory, runNsExec starts the child
// and places no mount.
func runNsExec(o nsExecOptions, cmdArgs []string) error {
	if len(cmdArgs) == 0 {
		return fmt.Errorf("__nsexec requires a command after '--'")
	}
	if o.socket != "" && o.scratch != "" {
		if err := o.mountRun(o.scratch); err != nil {
			return fmt.Errorf("the overlay mount on %s failed: %w; run the tailscale command through 'hydrascale exec' with the option --socket=%s", runMountPoint, err, o.socket)
		}
		if err := o.linkSocket(o.socket); err != nil {
			return err
		}
	}
	return o.execChild(cmdArgs)
}

// mountRunOverlay places a private tmpfs on scratch, then it places an overlay mount on
// /run that the tmpfs backs. A directory that the child creates under /run therefore
// lands on the tmpfs, and the host /run keeps the files that it holds now.
// The kernel frees the tmpfs when the last process of the mount namespace exits, so
// mountRunOverlay removes nothing.
// mountRunOverlay returns an error when it cannot create the scratch directory, when a
// mount call fails, and when /proc/self/mountinfo then holds no overlay mount on /run.
func mountRunOverlay(scratch string) error {
	if err := os.MkdirAll(scratch, 0700); err != nil {
		return fmt.Errorf("create the scratch directory: %w", err)
	}
	if err := syscall.Mount("tmpfs", scratch, "tmpfs", 0, "mode=0700"); err != nil {
		return fmt.Errorf("mount the tmpfs on %s: %w", scratch, err)
	}
	return mountOverlay(runMountPoint, filepath.Join(scratch, "upper"), filepath.Join(scratch, "work"))
}

// linkTailscaleSocket places the link for a tailnet at the location that the tailscale
// command expects.
func linkTailscaleSocket(socket string) error {
	return linkSocketAt(tailscaleSocketDir, tailscaleSocketPath, socket)
}

// linkSocketAt places a symbolic link at path that names the socket of the tailnet. The
// overlay mount on /run holds the link, so the link reaches no other mount namespace and
// it reaches no other tailnet.
// A host that runs the stock tailscaled holds a socket at path already, and the lower
// layer of the overlay mount shows it. linkSocketAt therefore removes path first. The
// removal writes a whiteout in the upper layer and the socket of the host stays.
// linkSocketAt returns an error when it cannot create the directory, when it cannot
// remove the existing path, and when it cannot create the link.
func linkSocketAt(dir, path, socket string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	if err := os.Symlink(socket, path); err != nil {
		return fmt.Errorf("link %s to %s: %w", path, socket, err)
	}
	return nil
}
