package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/request"
)

// ChatMessage represents a single message in a conversation (matching preprocessing.ChatMessage)
type ChatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // Can be string or []ContentBlock
}

// convertContentToPreprocessingFormat converts GAIE's Content structure to the format
// expected by preprocessing.ChatMessage. This matches the function in precise_prefix_cache.go
func convertContentToPreprocessingFormat(content types.Content) interface{} {
	// If structured content blocks are present (multi-modality), convert them to
	// the OpenAI API format expected by transformers library
	if len(content.Structured) > 0 {
		blocks := make([]map[string]interface{}, 0, len(content.Structured))
		for _, block := range content.Structured {
			blockMap := make(map[string]interface{})
			blockMap["type"] = block.Type

			if block.Type == "text" {
				blockMap["text"] = block.Text
			} else if block.Type == "image_url" {
				blockMap["image_url"] = map[string]interface{}{
					"url": block.ImageURL.Url,
				}
			}

			blocks = append(blocks, blockMap)
		}
		return blocks
	}

	// For text-only content, return the raw string (backward compatible)
	return content.Raw
}

func main() {
	fmt.Println("=== End-to-End Multi-Modality Pipeline Test ===\n")

	// Test 1: Image URL format
	testImageURL := map[string]any{
		"model": "test-model",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "text",
						"text": "What is in this image?",
					},
					{
						"type": "image_url",
						"image_url": map[string]any{
							"url": "https://example.com/image.jpg",
						},
					},
				},
			},
		},
	}

	fmt.Println("Test 1: Image URL Format")
	fmt.Println("Step 1: Parsing request with GAIE ExtractRequestBody...")
	body1, err := request.ExtractRequestBody(testImageURL)
	if err != nil {
		fmt.Printf("Error parsing request: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Request parsed successfully")

	fmt.Println("\nStep 2: Converting to preprocessing format...")
	if body1.ChatCompletions == nil {
		fmt.Println("ChatCompletions is nil")
		os.Exit(1)
	}

	preprocessingMessages := make([]ChatMessage, 0)
	for _, msg := range body1.ChatCompletions.Messages {
		content := convertContentToPreprocessingFormat(msg.Content)
		preprocessingMessages = append(preprocessingMessages, ChatMessage{
			Role:    msg.Role,
			Content: content,
		})
	}
	fmt.Printf("Converted %d messages to preprocessing format\n", len(preprocessingMessages))

	fmt.Println("\nStep 3: Verifying content structure...")
	for i, msg := range preprocessingMessages {
		fmt.Printf("   Message %d: Role=%s\n", i+1, msg.Role)
		contentJSON, err := json.Marshal(msg.Content)
		if err != nil {
			fmt.Printf("Failed to marshal content: %v\n", err)
		} else {
			fmt.Printf("Content: %s\n", string(contentJSON))
		}
	}

	// Test 2: Base64 format
	testBase64 := map[string]any{
		"model": "test-model",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "image_url",
						"image_url": map[string]any{
							"url": "data:image/jpeg;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVQImQEBAAAA//8AAAACAAEAAAAASUVORK5CYII=",
						},
					},
				},
			},
		},
	}

	fmt.Println("\n\nTest 2: Base64 Image Format")
	fmt.Println("Step 1: Parsing request with GAIE ExtractRequestBody...")
	body2, err := request.ExtractRequestBody(testBase64)
	if err != nil {
		fmt.Printf("Error parsing request: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Request parsed successfully")

	fmt.Println("\nStep 2: Converting to preprocessing format...")
	preprocessingMessages2 := make([]ChatMessage, 0)
	for _, msg := range body2.ChatCompletions.Messages {
		content := convertContentToPreprocessingFormat(msg.Content)
		preprocessingMessages2 = append(preprocessingMessages2, ChatMessage{
			Role:    msg.Role,
			Content: content,
		})
	}
	fmt.Printf("Converted %d messages to preprocessing format\n", len(preprocessingMessages2))

	fmt.Println("\nStep 3: Verifying content structure...")
	for i, msg := range preprocessingMessages2 {
		fmt.Printf("   Message %d: Role=%s\n", i+1, msg.Role)
		contentJSON, err := json.Marshal(msg.Content)
		if err != nil {
			fmt.Printf("Failed to marshal content: %v\n", err)
		} else {
			// Truncate base64 for display
			contentStr := string(contentJSON)
			if len(contentStr) > 150 {
				contentStr = contentStr[:150] + "..."
			}
			fmt.Printf("   ✅ Content: %s\n", contentStr)
		}
	}

	// Test 3: Text-only (backward compatibility)
	testTextOnly := map[string]any{
		"model": "test-model",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "Hello, world!",
			},
		},
	}

	fmt.Println("\n\nTest 3: Text-only (Backward Compatibility)")
	fmt.Println("Step 1: Parsing request with GAIE ExtractRequestBody...")
	body3, err := request.ExtractRequestBody(testTextOnly)
	if err != nil {
		fmt.Printf("Error parsing request: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Request parsed successfully")

	fmt.Println("\nStep 2: Converting to preprocessing format...")
	preprocessingMessages3 := make([]ChatMessage, 0)
	for _, msg := range body3.ChatCompletions.Messages {
		content := convertContentToPreprocessingFormat(msg.Content)
		preprocessingMessages3 = append(preprocessingMessages3, ChatMessage{
			Role:    msg.Role,
			Content: content,
		})
	}
	fmt.Printf("Converted %d messages to preprocessing format\n", len(preprocessingMessages3))

	fmt.Println("\nStep 3: Verifying content structure...")
	for i, msg := range preprocessingMessages3 {
		fmt.Printf("   Message %d: Role=%s\n", i+1, msg.Role)
		// For text-only, Content should be a string
		if contentStr, ok := msg.Content.(string); ok {
			fmt.Printf("   ✅ Content (string): %s\n", contentStr)
		} else {
			fmt.Printf("   ⚠️  Content is not a string: %T\n", msg.Content)
		}
	}

	fmt.Println("\n" + strings.Repeat("=", 42))
	fmt.Println("All end-to-end tests passed!")
}
