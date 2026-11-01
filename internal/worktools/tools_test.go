package worktools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/stretchr/testify/require"
)

func TestWorkToolsCannotCreateDelegateOrReportForAnotherSession(t *testing.T) {
	ctx, s := context.Background(), agentdata.New(t.TempDir())
	w, err := s.TrackWork(ctx, agentdata.WorkItem{SessionID: "owner", WorkUpdate: agentdata.WorkUpdate{Title: "报告"}})
	require.NoError(t, err)
	foreign := Tool{Store: s, SessionID: "other", Report: true}
	data, _ := json.Marshal(agentdata.WorkUpdate{Title: "报告", State: "waiting_review", Version: w.Version})
	_, err = foreign.Execute(ctx, data, nil)
	require.ErrorIs(t, err, agentdata.ErrNotFound)
	own := Tool{Store: s, SessionID: "owner", Report: true}
	_, err = own.Execute(ctx, json.RawMessage(`{"session_id":"other","title":"x","version":1}`), nil)
	require.Error(t, err)
	_, err = own.Execute(ctx, data, nil)
	require.NoError(t, err)
	_, err = own.Execute(ctx, data, nil)
	require.ErrorIs(t, err, agentdata.ErrConflict)
	reader := Tool{Store: s, SessionID: "owner"}
	result, err := reader.Execute(ctx, json.RawMessage(`{}`), nil)
	require.NoError(t, err)
	require.Contains(t, result.Content, "waiting_review")
}
