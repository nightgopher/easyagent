//go:build unix

package tools

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

const backgroundTrackingSupported = true

func lockOrphanLedger(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
func unlockOrphanLedger(f *os.File)     { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

func processAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func processGroupAlive(pgid int) bool {
	if pgid <= 1 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func killOrphanProcessGroup(rec orphanRecord) bool {
	if rec.PGID <= 1 || !validBackgroundToken(rec.Token) {
		return false
	}
	for _, pid := range groupMemberPIDs(rec.PGID) {
		if !processHasBackgroundToken(pid, rec.Token) {
			continue
		}
		if group, err := syscall.Getpgid(pid); err == nil && group == rec.PGID {
			return syscall.Kill(-rec.PGID, syscall.SIGKILL) == nil
		}
	}
	return false
}

func groupMemberPIDs(pgid int) []int {
	out, err := exec.Command("ps", "-axo", "pid=,pgid=").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, row := range strings.Split(string(out), "\n") {
		fields := strings.Fields(row)
		if len(fields) != 2 || fields[1] != strconv.Itoa(pgid) {
			continue
		}
		if pid, err := strconv.Atoi(fields[0]); err == nil && pid > 1 {
			pids = append(pids, pid)
		}
	}
	return pids
}

func processHasBackgroundToken(pid int, token string) bool {
	marker := backgroundProcessTokenEnv + "=" + token
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
		if err != nil {
			return false
		}
		for _, value := range bytes.Split(data, []byte{0}) {
			if string(value) == marker {
				return true
			}
		}
		return false
	}
	// macOS hides other processes' environments. An inherited fd holds a
	// private marker file instead; its random name also survives leader exit.
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "3", "-Fn").Output()
	if err != nil {
		return false
	}
	for _, value := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(value, "n") && strings.HasSuffix(value, "/.background-task-"+token) {
			return true
		}
	}
	return false
}
