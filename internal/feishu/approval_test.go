package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/stretchr/testify/require"
)

func approvalEvent(id, choice, actor, message string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: actor}, Context: &callback.Context{OpenMessageID: message, OpenChatID: "oc_private"},
		Action: &callback.CallBackAction{Value: map[string]any{"approval_id": id, "decision": choice}},
	}}
}

func TestApprovalCardDeliveryAndAuthenticatedOneTimeCallback(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprint(approved), func(t *testing.T) {
			var mu sync.Mutex
			var privateCards []string
			var deliveries int
			client := testCard(func(r *http.Request) (*http.Response, error) {
				mu.Lock()
				defer mu.Unlock()
				if strings.Contains(r.URL.Path, "/cards") {
					var body struct {
						Data string `json:"data"`
						Card struct {
							Data string `json:"data"`
						} `json:"card"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						return nil, err
					}
					if body.Data == "" {
						body.Data = body.Card.Data
					}
					privateCards = append(privateCards, body.Data)
					return cardResponse(`{"code":0,"data":{"card_id":"card-approval"}}`), nil
				}
				if strings.Contains(r.URL.Path, "/messages") {
					var body map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					require.Equal(t, "open_id", r.URL.Query().Get("receive_id_type"))
					require.Equal(t, "ou_owner", body["receive_id"])
					deliveries++
					return cardResponse(`{"code":0,"data":{"message_id":"om_approval"}}`), nil
				}
				return nil, fmt.Errorf("unexpected Feishu request %s", r.URL)
			}).client
			var calls int
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/sessions/session-owner/run/confirm", r.URL.Path)
				require.Equal(t, "Bearer core-key", r.Header.Get("Authorization"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, "run-owner", body["run_id"])
				require.Equal(t, "confirm-owner", body["confirmation_id"])
				require.Equal(t, approved, body["approved"])
				calls++
				if calls > 1 {
					w.WriteHeader(http.StatusConflict)
				}
				fmt.Fprint(w, `{}`)
			}))
			defer core.Close()
			owner, err := OpenOwnerAccess(filepath.Join(t.TempDir(), "owner.json"), "ou_owner")
			require.NoError(t, err)
			h := &Handler{access: owner, client: client, piAgentURL: core.URL, piAgentAPIKey: "core-key", httpClient: core.Client()}
			pending := toolApproval{ID: "confirm-owner", ToolName: "bash", Description: "echo <at id=all>private</at>", Args: json.RawMessage(`{"token":"secret-argument"}`)}
			require.NoError(t, h.sendToolApproval(context.Background(), "session-owner", "run-owner", pending))
			require.NoError(t, h.sendToolApproval(context.Background(), "session-owner", "run-owner", pending))
			require.Equal(t, 1, deliveries, "replayed confirmation must not send a second card")
			require.NotContains(t, privateCards[0], "secret-argument")
			require.NotContains(t, privateCards[0], "<at ")
			require.Contains(t, privateCards[0], `"behaviors"`)
			require.NotContains(t, privateCards[0], `"tag":"action"`)
			choice := "deny"
			if approved {
				choice = "allow"
			}
			for _, pair := range [][2]string{{"stranger", "om_approval"}, {"ou_owner", "wrong-message"}} {
				response, err := h.HandleCardAction(context.Background(), approvalEvent(pending.ID, choice, pair[0], pair[1]))
				require.NoError(t, err)
				require.NotEqual(t, "success", response.Toast.Type)
			}
			require.Zero(t, calls)
			response, err := h.HandleCardAction(context.Background(), approvalEvent(pending.ID, choice, "ou_owner", "om_approval"))
			require.NoError(t, err)
			require.Equal(t, "success", response.Toast.Type)
			response, err = h.HandleCardAction(context.Background(), approvalEvent(pending.ID, choice, "ou_owner", "om_approval"))
			require.NoError(t, err)
			require.Equal(t, "warning", response.Toast.Type)
			h.finishToolApproval(context.Background(), pending.ID, approved, "decision from core")
			require.NotContains(t, privateCards[len(privateCards)-1], `"behaviors"`)
			response, err = h.HandleCardAction(context.Background(), approvalEvent(pending.ID, choice, "ou_owner", "om_approval"))
			require.NoError(t, err)
			require.Equal(t, "warning", response.Toast.Type)
			require.Equal(t, 2, calls)
		})
	}
}

func TestQuestionApprovalRequiresAnswers(t *testing.T) {
	owner, err := OpenOwnerAccess(filepath.Join(t.TempDir(), "owner.json"), "ou_owner")
	require.NoError(t, err)
	p := &toolApproval{ID: "q", ToolName: "ask_user_question", messageID: "om_q", sessionID: "s", runID: "r", created: time.Now(), Args: json.RawMessage(`{"questions":[{"question":"Choose?","options":[{"label":"A"},{"label":"B"}]}]}`)}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Answers []struct {
				Text string `json:"text"`
			} `json:"answers"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Len(t, body.Answers, 1)
		require.Equal(t, "A", body.Answers[0].Text)
		fmt.Fprint(w, `{}`)
	}))
	defer core.Close()
	h := &Handler{access: owner, approvals: map[string]*toolApproval{"q": p}, piAgentURL: core.URL, httpClient: core.Client()}
	event := approvalEvent("q", "allow", "ou_owner", "om_q")
	response, err := h.HandleCardAction(context.Background(), event)
	require.NoError(t, err)
	require.Equal(t, "warning", response.Toast.Type)
	event.Event.Action.FormValue = map[string]any{"answer_0": "A"}
	response, err = h.HandleCardAction(context.Background(), event)
	require.NoError(t, err)
	require.Equal(t, "success", response.Toast.Type)
	card, err := json.Marshal(approvalCard(*p, ""))
	require.NoError(t, err)
	require.Contains(t, string(card), `"form_action_type":"submit"`)
}
