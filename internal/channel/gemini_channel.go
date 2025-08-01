package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

func init() {
	Register("gemini", newGeminiChannel)
}

type GeminiChannel struct {
	*BaseChannel
}

func newGeminiChannel(f *Factory, group *models.Group) (ChannelProxy, error) {
	base, err := f.newBaseChannel("gemini", group)
	if err != nil {
		return nil, err
	}

	return &GeminiChannel{
		BaseChannel: base,
	}, nil
}

// ModifyRequest adds the API key as a query parameter for Gemini requests.
func (ch *GeminiChannel) ModifyRequest(req *http.Request, apiKey *models.APIKey, group *models.Group) {
	if strings.Contains(req.URL.Path, "v1beta/openai") {
		req.Header.Set("Authorization", "Bearer "+apiKey.KeyValue)
	} else {
		q := req.URL.Query()
		q.Set("key", apiKey.KeyValue)
		req.URL.RawQuery = q.Encode()
	}
}

// IsStreamRequest checks if the request is for a streaming response.
func (ch *GeminiChannel) IsStreamRequest(c *gin.Context, bodyBytes []byte) bool {
	path := c.Request.URL.Path
	if strings.HasSuffix(path, ":streamGenerateContent") {
		return true
	}

	// Also check for standard streaming indicators as a fallback.
	if strings.Contains(c.GetHeader("Accept"), "text/event-stream") {
		return true
	}
	if c.Query("stream") == "true" {
		return true
	}

	type streamPayload struct {
		Stream bool `json:"stream"`
	}
	var p streamPayload
	if err := json.Unmarshal(bodyBytes, &p); err == nil {
		return p.Stream
	}

	return false
}

// ValidateKey checks if the given API key is valid by making a generateContent request.
func (ch *GeminiChannel) ValidateKey(ctx context.Context, key string) (bool, error) {
	upstreamURL := ch.getUpstreamURL()
	if upstreamURL == nil {
		return false, fmt.Errorf("no upstream URL configured for channel %s", ch.Name)
	}

	// Safely join the path segments
	reqURL, err := url.JoinPath(upstreamURL.String(), "v1beta", "models", ch.TestModel+":generateContent")
	if err != nil {
		return false, fmt.Errorf("failed to create gemini validation path: %w", err)
	}
	reqURL += "?key=" + key

	payload := gin.H{
		"contents": []gin.H{
			{"parts": []gin.H{
				{"text": "hi"},
			}},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("failed to marshal validation payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewBuffer(body))
	if err != nil {
		return false, fmt.Errorf("failed to create validation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := ch.HTTPClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to send validation request: %w", err)
	}
	defer resp.Body.Close()

	// A 200 OK status code indicates the key is valid.
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}

	// For non-200 responses, parse the body to provide a more specific error reason.
	errorBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("key is invalid (status %d), but failed to read error body: %w", resp.StatusCode, err)
	}

	// Use the new parser to extract a clean error message.
	parsedError := app_errors.ParseUpstreamError(errorBody)

	return false, fmt.Errorf("[status %d] %s", resp.StatusCode, parsedError)
}
