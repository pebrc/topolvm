package filesystem

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	blkidCmd = "/sbin/blkid"
)

func isSameDevice(dev1, dev2 string) (bool, error) {
	if dev1 == dev2 {
		return true, nil
	}

	var st1, st2 unix.Stat_t
	if err := unix.Stat(dev1, &st1); err != nil {
		return false, fmt.Errorf("stat failed for %s: %v", dev1, err)
	}
	if err := unix.Stat(dev2, &st2); err != nil {
		return false, fmt.Errorf("stat failed for %s: %v", dev2, err)
	}

	return st1.Rdev == st2.Rdev, nil
}

// isMounted returns true if device is mounted on target.
// The implementation uses /proc/mounts because btrfs uses a virtual device.
func isMounted(device, target string) (bool, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return false, err
	}
	target, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return false, err
	}

	data, err := ioutil.ReadFile("/proc/mounts")
	if err != nil {
		return false, fmt.Errorf("could not read /proc/mounts: %v", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		d, err := filepath.EvalSymlinks(fields[1])
		if err != nil {
			return false, err
		}
		if d == target {
			return isSameDevice(device, fields[0])
		}
	}

	return false, nil
}

// Mount mounts a block device onto target with filesystem-specific opts.
// target directory must exist.
func Mount(device, target, fsType, opts string, readonly bool) error {
	switch mounted, err := isMounted(device, target); {
	case err != nil:
		return err
	case mounted:
		return nil
	}

	var flg uintptr = unix.MS_LAZYTIME
	if readonly {
		flg |= unix.MS_RDONLY
	}
	return unix.Mount(device, target, fsType, flg, opts)
}

// Unmount unmounts the device if it is mounted.
func Unmount(device, target string) error {
	switch mounted, err := isMounted(device, target); {
	case err != nil:
		return err
	case !mounted:
		return nil
	}

	return unix.Unmount(target, unix.UMOUNT_NOFOLLOW)
}

// DetectFilesystem returns filesystem type if device has a filesystem.
// This returns an empty string if no filesystem exists.
func DetectFilesystem(device string) (string, error) {
	f, err := os.Open(device)
	if err != nil {
		return "", err
	}
	// synchronizes dirty data
	f.Sync()
	f.Close()

	out, err := exec.Command(blkidCmd, "-c", "/dev/null", "-o", "export", device).CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// blkid exists with status 2 when anything can be found
			if exitErr.ExitCode() == 2 {
				return "", nil
			}
		}
		return "", fmt.Errorf("blkid failed: output=%s, device=%s, error=%v", string(out), device, err)
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "TYPE=") {
			return line[5:], nil
		}
	}

	return "", nil
}
