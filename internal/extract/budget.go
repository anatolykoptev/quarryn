package extract

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// llmExtractCallsTotal counts wowa /extract calls actually placed —
	// the spend the daily cap bounds.
	llmExtractCallsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "quarryn",
			Name:      "llm_extract_calls_total",
			Help:      "wowa /api/v1/extract LLM fallback calls placed.",
		},
	)
	// llmBudgetExhaustedTotal counts candidates skipped by the daily spend
	// cap — they continue unenriched, so the counter is the only loud part.
	llmBudgetExhaustedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "quarryn",
			Name:      "llm_budget_exhausted_total",
			Help:      "Candidates that skipped the LLM fallback because the daily spend cap was reached.",
		},
	)
)

// dailyBudget is the process-local UTC-day call counter behind
// EXTRACT_LLM_DAILY_MAX (ADR-12 spend bound). One mutex, one day stamp —
// the count resets at UTC midnight and silently on process restart
// (documented limitation: persistence would need Redis, which buys nothing
// at a 50-call scale). now is the test seam for day-rollover tests.
type dailyBudget struct {
	mu   sync.Mutex
	day  string // UTC YYYY-MM-DD bucket stamp
	used int
	max  int
	now  func() time.Time
}

func newDailyBudget(maxCalls int) *dailyBudget {
	return &dailyBudget{max: maxCalls, now: time.Now}
}

// tryConsume reserves one LLM call for today; false means the day's
// budget is spent. A day rollover resets used before the check.
func (b *dailyBudget) tryConsume() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	today := b.now().UTC().Format("2006-01-02")
	if b.day != today {
		b.day = today
		b.used = 0
	}
	if b.used >= b.max {
		return false
	}
	b.used++
	return true
}
