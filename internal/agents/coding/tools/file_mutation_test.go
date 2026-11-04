package tools

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileMutationQueue_SameFileSerialized(t *testing.T) {
	q := NewFileMutationQueue()
	ctx := context.Background()

	var order []int
	var mu sync.Mutex
	counter := int32(0)

	// 启动 3 个对同一文件的操作
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := q.Execute(ctx, "/tmp/same_file.go", func() (agent.ToolResult, error) {
				n := atomic.AddInt32(&counter, 1)
				mu.Lock()
				order = append(order, int(n))
				mu.Unlock()
				time.Sleep(10 * time.Millisecond) // 模拟耗时操作
				return agent.ToolResult{}, nil
			})
			require.NoError(t, err)
		}(i)
	}
	wg.Wait()

	// 验证串行执行：order 应该是 [1, 2, 3]
	assert.Equal(t, []int{1, 2, 3}, order)
}
