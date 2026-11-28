package failover

import (
	"context"
	"crypto/sha256"
	"iter"
	"sync"
	"time"

	"github.com/Autumn-27/norma/llm"
)

// Admission shares configured rate limits across providers of the same service.
type serviceAdmission struct {
	mu    sync.Mutex
	rates map[[2]float64]*admissionRate
}

var admissions sync.Map

func coordinated(p llm.Provider, cfg ModelConfig) llm.Provider {
	key := sha256.Sum256([]byte(cfg.BaseURL + "\x00" + cfg.APIKey))
	entry, _ := admissions.LoadOrStore(key, &serviceAdmission{})
	a := entry.(*serviceAdmission)
	a.mu.Lock()
	if a.rates == nil {
		a.rates = make(map[[2]float64]*admissionRate)
	}
	ratesKey := [2]float64{cfg.RatePerSecond, cfg.RatePerMinute}
	rate := a.rates[ratesKey]
	if rate == nil {
		rate = &admissionRate{second: newAdmissionBucket(cfg.RatePerSecond, time.Second), minute: newAdmissionBucket(cfg.RatePerMinute, time.Minute)}
		a.rates[ratesKey] = rate
	}
	a.mu.Unlock()
	return coordinatedProvider{p, a, rate}
}

// Share the SDK's token-bucket windows and initial bursts across instances.
type admissionBucket struct {
	rate, capacity, tokens float64
	updated                time.Time
}

func newAdmissionBucket(count float64, window time.Duration) *admissionBucket {
	if count <= 0 {
		return nil
	}
	capacity := max(1, count)
	return &admissionBucket{rate: count / window.Seconds(), capacity: capacity, tokens: capacity, updated: time.Now()}
}
func (b *admissionBucket) delay(now time.Time) time.Duration {
	if b == nil {
		return 0
	}
	b.tokens = min(b.capacity, b.tokens+now.Sub(b.updated).Seconds()*b.rate)
	b.updated = now
	if b.tokens >= 1 {
		return 0
	}
	return time.Duration((1-b.tokens)/b.rate*float64(time.Second)) + time.Nanosecond
}

type admissionRate struct{ second, minute *admissionBucket }

func (r *admissionRate) delay(now time.Time) time.Duration {
	if r == nil {
		return 0
	}
	return max(r.second.delay(now), r.minute.delay(now))
}
func (r *admissionRate) take() {
	if r == nil {
		return
	}
	if r.second != nil {
		r.second.tokens--
	}
	if r.minute != nil {
		r.minute.tokens--
	}
}

func (a *serviceAdmission) wait(ctx context.Context, rate *admissionRate) error {
	for {
		a.mu.Lock()
		if err := ctx.Err(); err != nil {
			a.mu.Unlock()
			return err
		}
		delay := rate.delay(time.Now())
		if delay <= 0 {
			rate.take()
			a.mu.Unlock()
			return nil
		}
		a.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type coordinatedProvider struct {
	inner     llm.Provider
	admission *serviceAdmission
	rate      *admissionRate
}

func (p coordinatedProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	err := p.admission.wait(ctx, p.rate)
	if err != nil {
		noUsageAttempt(ctx)
		return llm.Message{}, "", llm.Usage{}, err
	}
	return p.inner.Complete(ctx, req)
}
func (p coordinatedProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		err := p.admission.wait(ctx, p.rate)
		if err != nil {
			noUsageAttempt(ctx)
			yield(llm.StreamEvent{}, err)
			return
		}
		for event, err := range p.inner.Stream(ctx, req) {
			if !yield(event, err) {
				return
			}
		}
	}
}
