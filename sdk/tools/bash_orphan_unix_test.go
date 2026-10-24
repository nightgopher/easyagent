//go:build unix

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const orphanTestToken = "0123456789abcdef0123456789abcdef"

func startDetached(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), backgroundProcessTokenEnv+"="+orphanTestToken)
	marker, err := os.OpenFile(filepath.Join(t.TempDir(), ".background-task-"+orphanTestToken), os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	defer marker.Close()
	cmd.ExtraFiles = []*os.File{marker}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	require.NoError(t, cmd.Start())
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return cmd
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	return cmd.Process.Pid
}

func (l *orphanLedger) snapshot() []orphanRecord {
	if l == nil {
		return nil
	}
	data, _ := os.ReadFile(l.path)
	var records []orphanRecord
	_ = json.Unmarshal(data, &records)
	return records
}

func TestOrphanCleanupKillsLeftoverGroup(t *testing.T) {
	ledger := newOrphanLedger(t.TempDir())
	dead := deadPID(t)
	orphan := startDetached(t, "sleep", "30")
	require.NoError(t, ledger.add(orphanRecord{PGID: orphan.Process.Pid, OwnerPID: dead, Token: orphanTestToken, StartedAt: time.Now().Unix()}))
	live := startDetached(t, "sleep", "30")
	require.NoError(t, ledger.add(orphanRecord{PGID: live.Process.Pid, OwnerPID: os.Getpid(), Token: orphanTestToken}))
	require.NoError(t, ledger.cleanup())
	require.Eventually(t, func() bool { return !processAlive(orphan.Process.Pid) }, 3*time.Second, 20*time.Millisecond)
	assert.True(t, processAlive(live.Process.Pid))
	require.Len(t, ledger.snapshot(), 1)
	assert.Equal(t, live.Process.Pid, ledger.snapshot()[0].PGID)
}

func TestOrphanCleanupDoesNotKillReusedGroupWithSameCommand(t *testing.T) {
	ledger := newOrphanLedger(t.TempDir())
	stranger := startDetached(t, "sleep", "30")
	require.NoError(t, ledger.add(orphanRecord{PGID: stranger.Process.Pid, OwnerPID: deadPID(t), Token: "ffffffffffffffffffffffffffffffff", StartedAt: 1}))
	require.NoError(t, ledger.cleanup())
	assert.True(t, processAlive(stranger.Process.Pid), "a matching command is insufficient proof of process identity")
	require.Len(t, ledger.snapshot(), 1, "retain unverifiable records for diagnosis")
}

func TestOrphanCleanupGroupSurvivesLeaderDeath(t *testing.T) {
	ledger := newOrphanLedger(t.TempDir())
	orphan := startDetached(t, "sh", "-c", "sleep 30 & sleep 0.1")
	require.NoError(t, ledger.add(orphanRecord{PGID: orphan.Process.Pid, OwnerPID: deadPID(t), Token: orphanTestToken}))
	require.Eventually(t, func() bool { return !processAlive(orphan.Process.Pid) }, 3*time.Second, 20*time.Millisecond)
	require.NoError(t, ledger.cleanup())
	require.Eventually(t, func() bool { return !processGroupAlive(orphan.Process.Pid) }, 3*time.Second, 20*time.Millisecond)
	assert.Empty(t, ledger.snapshot())
}

func TestOrphanLedgerRecordLifecycle(t *testing.T) {
	tasks := NewBackgroundTasks(WithBackgroundDataDir(t.TempDir()))
	ops := stubBashOps{run: func(ctx context.Context, req operations.RunRequest) (operations.RunResult, error) {
		require.Len(t, req.Env, 1)
		req.OnStart(4322)
		<-ctx.Done()
		return operations.RunResult{ExitCode: -1}, ctx.Err()
	}}
	task, err := tasks.start(ops, "sleep 30", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(tasks.ledger.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
	task.kill()
	<-task.done
	assert.Empty(t, tasks.ledger.snapshot())
}

type stubBashOps struct {
	run func(context.Context, operations.RunRequest) (operations.RunResult, error)
}

func (s stubBashOps) Run(ctx context.Context, req operations.RunRequest) (operations.RunResult, error) {
	return s.run(ctx, req)
}

func TestLocalOpsReportsOnStart(t *testing.T) {
	var got int
	res, err := operations.LocalBashOperations{}.Run(context.Background(), operations.RunRequest{
		Command: "printf %s \"$EA_TEST_CHILD_ENV\"",
		Env:     []string{"EA_TEST_CHILD_ENV=present"},
		OnStart: func(pid int) { got = pid },
	})
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "present", string(res.Output))
	assert.Greater(t, got, 0)
}

func TestOrphanLedgerMultipleInstancesPreserveRecords(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- newOrphanLedger(dir).add(orphanRecord{PGID: i + 100, OwnerPID: os.Getpid(), Token: fmt.Sprintf("%032x", i)})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, newOrphanLedger(dir).snapshot(), 40)
	info, err := os.Stat(filepath.Join(dir, "background-tasks.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestOrphanRecoveryAfterSIGKILL(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	require.NoError(t, err)
	owner := exec.Command(exe, "-test.run=^TestOrphanProcessHelper$")
	owner.Env = append(os.Environ(), "EA_TEST_ORPHAN_DIR="+dir, "EA_TEST_ORPHAN_MODE=spawn")
	require.NoError(t, owner.Start())
	ledger := newOrphanLedger(dir)
	t.Cleanup(func() {
		_ = owner.Process.Kill()
		for _, rec := range ledger.snapshot() {
			_ = syscall.Kill(-rec.PGID, syscall.SIGKILL)
		}
	})
	require.Eventually(t, func() bool { return len(ledger.snapshot()) == 1 }, 5*time.Second, 20*time.Millisecond)
	rec := ledger.snapshot()[0]
	require.Eventually(t, func() bool { return processHasBackgroundToken(rec.PGID, rec.Token) }, 3*time.Second, 20*time.Millisecond)
	require.NoError(t, owner.Process.Kill())
	_ = owner.Wait()
	require.True(t, processGroupAlive(rec.PGID), "SIGKILL must leave an actual orphan before recovery")
	restart := exec.Command(exe, "-test.run=^TestOrphanProcessHelper$")
	restart.Env = append(os.Environ(), "EA_TEST_ORPHAN_DIR="+dir, "EA_TEST_ORPHAN_MODE=recover")
	out, err := restart.CombinedOutput()
	require.NoError(t, err, string(out))
	require.Eventually(t, func() bool { return !processGroupAlive(rec.PGID) }, 5*time.Second, 20*time.Millisecond)
	assert.Empty(t, ledger.snapshot())
}

func TestOrphanProcessHelper(t *testing.T) {
	dir := os.Getenv("EA_TEST_ORPHAN_DIR")
	if dir == "" {
		return
	}
	if os.Getenv("EA_TEST_ORPHAN_MODE") == "recover" {
		CleanupBackgroundTasks(dir)
		return
	}
	tasks := NewBackgroundTasks(WithBackgroundDataDir(dir))
	_, err := tasks.start(operations.LocalBashOperations{}, "sleep 60", "")
	require.NoError(t, err)
	select {}
}
