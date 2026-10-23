package ai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransformMessages_ImageDowngrade(t *testing.T) {
	msgs := []Message{
		UserMessage{Content: []ContentBlock{
			{Type: "text", Text: "Hello"},
			{Type: "image", Image: &ImageBlock{URL: "http://example.com/img.png"}},
		}},
	}

	opts := DefaultTransformOptions()
	opts.NoImageSupport = true
	result := TransformMessages(msgs, opts)

	user := result[0].(UserMessage)
	assert.Equal(t, 2, len(user.Content))
	assert.Equal(t, "text", user.Content[0].Type)
	assert.Equal(t, ImageUnsupportedMessage, user.Content[1].Text)
}

func TestTransformMessages_NoImageDowngrade(t *testing.T) {
	msgs := []Message{
		UserMessage{Content: []ContentBlock{
			{Type: "text", Text: "Hello"},
			{Type: "image", Image: &ImageBlock{URL: "http://example.com/img.png"}},
		}},
	}

	opts := DefaultTransformOptions()
	opts.NoImageSupport = false
	result := TransformMessages(msgs, opts)

	user := result[0].(UserMessage)
	assert.Equal(t, 2, len(user.Content))
	assert.Equal(t, "image", user.Content[1].Type)
}

func TestTransformMessages_NormalizeToolCallIDs(t *testing.T) {
	msgs := []Message{
		AssistantMessage{
			Text: "using tool",
			ToolCalls: []ToolCall{
				{ID: "", Name: "bash", Args: `{"command":"ls"}`},
			},
		},
	}

	opts := DefaultTransformOptions()
	opts.NormalizeToolCallIDs = true
	result := TransformMessages(msgs, opts)

	assistant := result[0].(AssistantMessage)
	assert.NotEmpty(t, assistant.ToolCalls[0].ID)
	assert.Contains(t, assistant.ToolCalls[0].ID, "generated_tc_")
}

func TestTransformMessages_MergeConsecutive(t *testing.T) {
	msgs := []Message{
		NewTextUserMessage("msg1"),
		NewTextUserMessage("msg2"),
	}

	opts := DefaultTransformOptions()
	opts.MergeConsecutiveRoles = true
	result := TransformMessages(msgs, opts)

	assert.Len(t, result, 1)
	user := result[0].(UserMessage)
	assert.Equal(t, 2, len(user.Content))
}

func TestTransformMessages_Empty(t *testing.T) {
	result := TransformMessages(nil, DefaultTransformOptions())
	assert.Nil(t, result)
}

func TestValidateMessageSequence_Valid(t *testing.T) {
	msgs := []Message{
		NewTextUserMessage("hi"),
		AssistantMessage{Text: "hello"},
		NewTextUserMessage("how are you?"),
	}
	err := ValidateMessageSequence(msgs)
	assert.NoError(t, err)
}

func TestValidateMessageSequence_ConsecutiveAssistant(t *testing.T) {
	msgs := []Message{
		NewTextUserMessage("hi"),
		AssistantMessage{Text: "hello"},
		AssistantMessage{Text: "world"},
	}
	err := ValidateMessageSequence(msgs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "consecutive assistant")
}

func TestValidateMessageSequence_ToolResultFollowedByUser(t *testing.T) {
	msgs := []Message{
		NewTextUserMessage("hi"),
		AssistantMessage{Text: "let me check", ToolCalls: []ToolCall{{ID: "tc1", Name: "bash", Args: `{}`}}},
		ToolResultMessage{ToolCallID: "tc1", Content: "ok"},
		NewTextUserMessage("next question"),
	}
	err := ValidateMessageSequence(msgs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be followed by assistant")
}

func TestValidateMessageSequence_Empty(t *testing.T) {
	err := ValidateMessageSequence(nil)
	assert.NoError(t, err)
}

func TestMessagesToJSON(t *testing.T) {
	msgs := []Message{
		NewTextUserMessage("hello"),
		AssistantMessage{Text: "world"},
	}
	json, err := MessagesToJSON(msgs)
	require.NoError(t, err)
	assert.Contains(t, json, "hello")
	assert.Contains(t, json, "world")
}

func TestModelImageDowngradePreservesHistoryAndMetadata(t *testing.T) {
	no := false
	original := []Message{
		UserMessage{DisplayText: "uploaded image", Content: []ContentBlock{{Type: "image", Image: &ImageBlock{Data: []byte{1, 2}}}}},
		ToolResultMessage{ToolCallID: "read-1", Content: "file.png", DurationMS: 9, Images: []ImageBlock{{Data: []byte{3, 4}}}},
	}
	result := MessagesForModel(original, Model{SupportsImages: &no})
	user := result[0].(UserMessage)
	require.Equal(t, "uploaded image", user.DisplayText)
	require.Equal(t, "text", user.Content[0].Type)
	require.Equal(t, ImageUnsupportedMessage, user.Content[0].Text)
	tool := result[1].(ToolResultMessage)
	require.Empty(t, tool.Images)
	require.Contains(t, tool.Content, ImageUnsupportedMessage)
	require.Equal(t, "read-1", tool.ToolCallID)
	require.EqualValues(t, 9, tool.DurationMS)
	require.Equal(t, "image", original[0].(UserMessage).Content[0].Type)
	require.Len(t, original[1].(ToolResultMessage).Images, 1)
	for _, flag := range []*bool{nil, new(bool)} {
		if flag != nil {
			*flag = true
		}
		require.Equal(t, original, MessagesForModel(original, Model{SupportsImages: flag}))
	}
	require.Nil(t, ImageSupport(nil, nil))
	require.True(t, *ImageSupport(nil, []string{"text", "image"}))
	require.False(t, *ImageSupport(nil, []string{"text"}))
	require.False(t, *ImageSupport(&no, []string{"image"}), "explicit false wins")
}
