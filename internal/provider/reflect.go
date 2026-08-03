package provider

import (
	"os"
	"strings"
)

// ReflectProvider 回傳跑【背景反思】用的 provider：設了 COGITO_REFLECT_MODEL 就換成該模型的變體，
// 否則原樣沿用主 provider。
//
// 【為何值得】技能／記憶／KG 蒸餾是任務結束後的背景工作——沒有人在等它、產物還要過人工放行，
// 用主模型（常是 opus 級）跑純屬浪費。同型作法在 Hermes 的背景 review 實測省 3–5×。
//
// 刻意【不】涵蓋 goal judge：那道驗收決定任務要不要繼續跑，降級模型會直接影響任務結果，
// 與「省背景成本」是兩件事。provider 不支援 Configurable 時靜默沿用主 provider（行為不變）。
func ReflectProvider(p LLMProvider) LLMProvider {
	model := strings.TrimSpace(os.Getenv("COGITO_REFLECT_MODEL"))
	if model == "" || p == nil {
		return p
	}
	cfg, ok := p.(Configurable)
	if !ok {
		return p
	}
	return cfg.Configure(model, 0) // maxTokens 0＝沿用預設
}
