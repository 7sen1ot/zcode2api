package protocol

import (
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
	"zcode2api/internal/server/shared"
)

// EstimateInputTokens counts the same inputs the gateway prepares for inference.
func EstimateInputTokens(request shared.M) int {
	tokens := estimateContentTokens(request["system"], false)
	if tools := shared.Arr(request["tools"]); len(tools) > 0 {
		tokens += estimateTextTokens(shared.JSON(tools))
	}
	// Tool results continue the same assistant turn. Its visible thinking
	// remains part of the input until a new user prompt starts another turn.
	currentTurn := true
	for _, value := range slices.Backward(shared.Arr(request["messages"])) {
		message := shared.Obj(value)
		content := message["content"]
		if currentTurn && message["role"] == "user" {
			currentTurn = slices.ContainsFunc(shared.Arr(content), func(block any) bool {
				return shared.Obj(block)["type"] == "tool_result"
			})
		}
		tokens += 4 + estimateContentTokens(content, currentTurn && message["role"] == "assistant")
	}
	return max(1, tokens)
}

func estimateTextTokens(text string) int {
	return int(math.Ceil(float64(utf8.RuneCountInString(text)) / 2.5))
}

func estimateContentTokens(content any, countThinking bool) int {
	if text, ok := content.(string); ok {
		return estimateTextTokens(text)
	}
	tokens := 0
	for _, value := range shared.Arr(content) {
		block := shared.Obj(value)
		source := shared.Obj(block["source"])
		switch block["type"] {
		case "text":
			tokens += estimateTextTokens(shared.Str(block["text"]))
		case "tool_result":
			tokens += 4 + estimateContentTokens(block["content"], false)
		case "image":
			tokens += estimateImageTokens(source)
		case "document":
			switch source["type"] {
			case "text":
				tokens += estimateTextTokens(shared.Str(source["data"]))
			case "content":
				tokens += estimateContentTokens(source["content"], false)
			default:
				// Unknown PDFs use a per-page estimate scaled by the available size.
				const bytesPerPage = 128 << 10
				size := base64.StdEncoding.DecodedLen(len(shared.Str(source["data"])))
				tokens += 2200 * max(1, (size+bytesPerPage-1)/bytesPerPage)
			}
			tokens += estimateTextTokens(shared.Str(block["title"])) + estimateTextTokens(shared.Str(block["context"]))
		case "thinking":
			if countThinking {
				tokens += estimateTextTokens(shared.Str(block["thinking"]))
			}
		case "redacted_thinking":
			// The encrypted payload does not expose a text token count.
		default:
			tokens += estimateTextTokens(shared.JSON(block))
		}
	}
	return tokens
}

func estimateImageTokens(source shared.M) int {
	if source["type"] == "base64" {
		data := strings.NewReader(shared.Str(source["data"]))
		reader := base64.NewDecoder(base64.StdEncoding, data)
		info, _, err := image.DecodeConfig(io.LimitReader(reader, 64<<10))
		if err == nil && info.Width > 0 && info.Height > 0 {
			width, height := float64(info.Width), float64(info.Height)
			pixels := width * height
			scale := min(1, 1568/max(width, height), math.Sqrt(1150000/pixels))
			return int(math.Ceil(pixels * scale * scale / 750))
		}
	}
	return 1600
}
