package stewardtools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/stretchr/testify/require"
)

func TestWorkClosePreservesWorkAndHonorsVersionAndPermission(t *testing.T) {
	ctx := context.Background()
	store := agentdata.New(t.TempDir())
	steward, err := store.EnsureSteward(ctx)
	require.NoError(t, err)
	w, err := store.TrackWork(ctx, agentdata.WorkItem{SessionID: "session-1", Application: "coding", WorkUpdate: agentdata.WorkUpdate{Title: "报告", Summary: "原成果", Artifacts: []agentdata.WorkArtifact{{Title: "文件", Kind: "file", Value: "report.md"}}}})
	require.NoError(t, err)
	tool := WorkCloseTool{Store: store}
	args := func(version int64, state string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"work_id":%q,"version":%d,"state":%q}`, w.ID, version, state))
	}
	_, err = tool.Execute(ctx, args(w.Version+1, "completed"), nil)
	require.ErrorIs(t, err, agentdata.ErrConflict)
	_, err = tool.Execute(ctx, args(w.Version, "active"), nil)
	require.Error(t, err)
	result, err := tool.Execute(ctx, args(w.Version, "completed"), nil)
	require.NoError(t, err)
	var closed agentdata.WorkItem
	require.NoError(t, json.Unmarshal([]byte(result.Content), &closed))
	require.Equal(t, "completed", closed.State)
	require.Equal(t, "steward", closed.UpdatedBy)
	require.Equal(t, w.Summary, closed.Summary)
	require.Equal(t, w.Artifacts, closed.Artifacts)
	require.Equal(t, w.SessionID, closed.SessionID)
	_, err = tool.Execute(ctx, args(closed.Version, "cancelled"), nil)
	require.ErrorContains(t, err, "already closed")
	other, err := store.TrackWork(ctx, agentdata.WorkItem{SessionID: "session-2", Application: "coding", WorkUpdate: agentdata.WorkUpdate{Title: "取消的任务"}})
	require.NoError(t, err)
	cancelArgs := json.RawMessage(fmt.Sprintf(`{"work_id":%q,"version":%d,"state":"cancelled"}`, other.ID, other.Version))
	_, err = tool.Execute(ctx, cancelArgs, nil)
	require.NoError(t, err)
	other, err = store.GetWork(ctx, other.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", other.State)
	steward.Tools = []string{"local_time"}
	_, err = store.SaveAgent(ctx, steward)
	require.NoError(t, err)
	_, err = tool.Execute(ctx, args(closed.Version, "completed"), nil)
	require.ErrorContains(t, err, "permission is disabled")
}
