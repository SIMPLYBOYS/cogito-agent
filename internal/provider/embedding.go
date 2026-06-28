package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// OpenAIEmbedder 用 OpenAI 相容的 /embeddings 端點把文字轉向量（手寫、無 SDK，沿用多 Provider DNA）。
// Anthropic 無 embeddings 端點，故語意檢索一律經 OpenAI 相容端點（雲端或本地 Ollama/vLLM 皆可，維持自託管）。
type OpenAIEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// EmbedderFromEnv 未設 COGITO_EMBED_MODEL 時回 nil（→ recall 退回關鍵字，零依賴、零成本）。
// base/key 預設沿用 OPENAI_BASE_URL / OPENAI_API_KEY，可用 COGITO_EMBED_* 覆蓋。
func EmbedderFromEnv() *OpenAIEmbedder {
	model := os.Getenv("COGITO_EMBED_MODEL")
	if model == "" {
		return nil
	}
	base := firstNonEmpty(os.Getenv("COGITO_EMBED_BASE_URL"), os.Getenv("OPENAI_BASE_URL"), "https://api.openai.com/v1")
	key := firstNonEmpty(os.Getenv("COGITO_EMBED_API_KEY"), os.Getenv("OPENAI_API_KEY"))
	return &OpenAIEmbedder{
		baseURL: strings.TrimRight(base, "/"),
		apiKey:  key,
		model:   model,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// EmbedQuery 嵌入單一查詢字串。
func (e *OpenAIEmbedder) EmbedQuery(text string) ([]float32, error) {
	vs, err := e.Embed([]string{text})
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, fmt.Errorf("embeddings: 空回應")
	}
	return vs[0], nil
}

// Embed 批次嵌入（回傳順序對應輸入順序）。
// ponytail: 一次一個請求；輸入極多時再分批。
func (e *OpenAIEmbedder) Embed(texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": e.model, "input": texts})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings 請求失敗: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 embeddings 回應失敗: %w", err)
	}
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
