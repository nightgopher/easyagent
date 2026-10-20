package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_SetDefaultModel(t *testing.T) {
	application := newTestApp(t)
	srv := New(application, nil)

	// 1. 初始 GET /models 确认默认模型存在
	req := localReq(http.MethodGet, "/models", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var initialResp ModelsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &initialResp))
	assert.NotNil(t, initialResp.Current)

	// 2. 发送 PUT /models/default 更新全局默认模型
	newModel := "custom-default-model-42"
	putBody, _ := json.Marshal(map[string]string{
		"provider": "openai",
		"model":    newModel,
	})
	putReq := localReq(http.MethodPut, "/models/default", bytes.NewReader(putBody))
	putReq.Header.Set("Content-Type", "application/json")
	putW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(putW, putReq)
	require.Equal(t, http.StatusOK, putW.Code)

	var putResp map[string]any
	require.NoError(t, json.Unmarshal(putW.Body.Bytes(), &putResp))
	assert.Equal(t, true, putResp["ok"])
	assert.Equal(t, newModel, putResp["model"])

	// 3. 再次 GET /models，current 随之更新为新模型
	getReq := localReq(http.MethodGet, "/models", nil)
	getW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getW, getReq)
	require.Equal(t, http.StatusOK, getW.Code)

	var updatedResp ModelsResponse
	require.NoError(t, json.Unmarshal(getW.Body.Bytes(), &updatedResp))
	require.NotNil(t, updatedResp.Current)
	assert.Equal(t, newModel, updatedResp.Current.ID)

	// 4. 新建会话默认使用新模型
	sess, err := application.NewSession(context.Background())
	require.NoError(t, err)
	_, sessModel := sess.ModelInfo()
	assert.Equal(t, newModel, sessModel)

	// 5. 会话级覆盖不干扰全局默认
	switchBody, _ := json.Marshal(map[string]string{
		"model": "session-override-model",
	})
	switchReq := localReq(http.MethodPost, "/sessions/"+sess.SessionID()+"/model", bytes.NewReader(switchBody))
	switchReq.Header.Set("Content-Type", "application/json")
	switchW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(switchW, switchReq)
	require.Equal(t, http.StatusOK, switchW.Code)

	// 会话模型被修改
	_, updatedSessModel := sess.ModelInfo()
	assert.Equal(t, "session-override-model", updatedSessModel)

	// 全局默认仍是 newModel
	assert.Equal(t, newModel, application.Config().OpenAIModel)
}
