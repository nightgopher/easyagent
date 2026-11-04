package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/egoist/mygo/ui"
)

type messageImage struct {
	DataURL      string `json:"data_url"`
	AttachmentID string `json:"attachmentId"`
	SessionID    string `json:"sessionId"`
}

func (b *backendClient) download(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", b.URL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	res, err := b.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("媒体请求失败（HTTP %d）", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20+1))
	if len(data) > 32<<20 {
		return nil, errors.New("媒体超过 32 MiB 限制")
	}
	return data, err
}
func (a *preview) messageImages(c *ui.Context, m *message) {
	for i, item := range m.Images {
		key := item.DataURL
		if key == "" {
			key = a.Live.Config.ActiveHost + "/" + a.current().RemoteID + "/" + item.AttachmentID
		}
		a.imageThumbnail(c.Key(i), key, "附件图片", item, a.current().RemoteID)
	}
}
