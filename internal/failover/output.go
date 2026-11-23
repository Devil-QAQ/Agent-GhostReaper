package failover

import (
	"context"
	"github.com/Autumn-27/norma/llm"
	"iter"
)

// LibraryOutput 将会话引擎的缺省上限交给模型库；库内 capTokensProvider 按实际服务腿设置。
func LibraryOutput(inner llm.Provider) llm.Provider { return libraryOutput{inner} }

type libraryOutput struct{ inner llm.Provider }

func (p libraryOutput) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	req.MaxTokens = 0
	return p.inner.Complete(ctx, req)
}
func (p libraryOutput) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	req.MaxTokens = 0
	return p.inner.Stream(ctx, req)
}
