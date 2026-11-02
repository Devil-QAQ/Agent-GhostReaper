package failover

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// ---- 错误分类(第七章「故障与恢复语义」重试分层的判定面) ----
//
// 依 Norma 现状查证(v0.4.1):① SDK 建连重试(连接重置/超时/429/5xx,
// 指数退避)与 ② 空响应重放(请求通但零输出,原样重发,OpenAI 格式)
// 已由内置 Provider 自带(llm.Config.MaxRetries / EmptyResponseRetries);
// 本包补 ③ 零交付安全重放与 ④ 主备熔断切换(见 chain.go)。分类决定
// 一次失败落到哪一层:
//
//   - 额度耗尽(quota):不重试,直接切备用(交下层);
//   - 上下文超长(too long):不重试、不切备,原样上抛(交引擎压缩);
//   - 4xx 确定性拒绝(认证/参数错等):不重试、不切备,原样上抛;
//   - 调用方取消(canceled):立即上抛;
//   - 其余(建连失败/流断/5xx/429 耗尽重试):瞬时类,计连续失败、
//     可安全重放。

// ErrClass 一次模型调用失败的类别。
type ErrClass string

const (
	ClassTransient   ErrClass = "transient"   // 瞬时失败(可重放、计入熔断)
	ClassQuota       ErrClass = "quota"       // 额度耗尽(切备用)
	ClassTooLong     ErrClass = "too_long"    // 上下文超长(交压缩)
	ClassDeterminant ErrClass = "determinant" // 4xx 确定性拒绝(上抛)
	ClassCanceled    ErrClass = "canceled"    // 调用方取消(上抛)
)

// statusRe 从 Norma 错误文案里抠 HTTP 状态码(形如 "…: status 402: …")。
var statusRe = regexp.MustCompile(`status (\d{3})`)

// quotaWords 额度耗尽的错误特征(响应体片段或状态码,小写匹配)。
var quotaWords = []string{
	"insufficient_quota", "insufficient quota", "quota", "billing",
	"balance", "arrear", "payment required", "exceeded your current quota",
	"额度", "欠费",
}

// tooLongWords 上下文超长的错误特征。
var tooLongWords = []string{
	"context_length_exceeded", "context length", "maximum context",
	"prompt is too long", "request too large", "input too long",
	"too long", "上下文超长",
}

// Classify 按错误文本与调用方 ctx 判定类别。判定顺序:取消 > 状态码
// 精确分类 > 文本特征 > 默认瞬时(fail-safe:未知错误当瞬时,宁可多
// 一次安全重放,不误伤可恢复故障)。
func Classify(ctx context.Context, err error) ErrClass {
	if err == nil {
		return ClassTransient
	}
	if ctx != nil && ctx.Err() != nil {
		return ClassCanceled
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ClassCanceled
	}
	msg := strings.ToLower(err.Error())
	if m := statusRe.FindStringSubmatch(msg); m != nil {
		code, _ := strconv.Atoi(m[1])
		switch {
		case code == http.StatusPaymentRequired: // 402
			return ClassQuota
		case code == http.StatusTooManyRequests: // 429:重试耗尽后到此,按瞬时
			return ClassTransient
		case code >= 400 && code < 500:
			return ClassDeterminant
		case code >= 500:
			return ClassTransient
		}
	}
	for _, w := range quotaWords {
		if strings.Contains(msg, w) {
			return ClassQuota
		}
	}
	for _, w := range tooLongWords {
		if strings.Contains(msg, w) {
			return ClassTooLong
		}
	}
	return ClassTransient
}
