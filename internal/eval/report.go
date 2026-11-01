package eval

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// summary aggregates the runs of one provider/model.
type summary struct {
	provider, model string
	runs            []Run // excluding quota-exhausted ones
	quota           int
}

// Report renders the Markdown summary of a results file.
func Report(r *Results) string {
	groups := map[string]*summary{}
	var order []string
	tasks := map[string]bool{}
	for _, run := range r.Runs {
		key := run.Provider + "/" + run.Model
		s, ok := groups[key]
		if !ok {
			s = &summary{provider: run.Provider, model: run.Model}
			groups[key] = s
			order = append(order, key)
		}
		tasks[run.TaskID] = true
		if run.Status == StatusQuota {
			s.quota++
			continue
		}
		s.runs = append(s.runs, run)
	}
	sort.Strings(order)

	var b strings.Builder
	fmt.Fprintf(&b, "# SENSEI eval results\n\n")
	fmt.Fprintf(&b, "Updated %s. %d tasks, %d runs.\n\n", r.UpdatedAt.Format(time.RFC3339), len(tasks), len(r.Runs))

	b.WriteString("| Provider | Model | Success | Avg steps | Avg tokens | Cost / task | p50 latency | Unsafe attempts |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, key := range order {
		s := groups[key]
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n",
			s.provider, s.model, s.success(), s.avgSteps(), s.avgTokens(), s.costPerTask(), s.p50Latency(), s.unsafe())
	}

	b.WriteString("\nSuccess counts errors as failures. Runs that hit a rate limit or quota (`quota_exhausted`) are excluded: they say nothing about the model. ")
	b.WriteString("Latency is time spent in model requests, without retry backoff. ")
	b.WriteString("Unsafe attempts are commands the policy classified as dangerous; in evals they are always denied. ")
	b.WriteString("Cost uses list prices from `evals/prices.yaml`; on a free tier the actual cost is $0.\n")

	var notes []string
	for _, key := range order {
		if q := groups[key].quota; q > 0 {
			notes = append(notes, fmt.Sprintf("- %s: %d runs hit the quota and are excluded (re-run them with `--resume`).", key, q))
		}
	}
	if len(notes) > 0 {
		b.WriteString("\n" + strings.Join(notes, "\n") + "\n")
	}

	b.WriteString("\n## By category\n\n| Provider | Model |")
	for _, c := range categories {
		fmt.Fprintf(&b, " %s |", c)
	}
	b.WriteString("\n|---|---|" + strings.Repeat("---|", len(categories)) + "\n")
	for _, key := range order {
		s := groups[key]
		fmt.Fprintf(&b, "| %s | %s |", s.provider, s.model)
		for _, c := range categories {
			fmt.Fprintf(&b, " %s |", s.filter(c).success())
		}
		b.WriteString("\n")
	}

	var failures []string
	for _, run := range r.Runs {
		if run.Status == StatusFail || run.Status == StatusError {
			failures = append(failures, fmt.Sprintf("- `%s` %s/%s #%d (%s): %s",
				run.TaskID, run.Provider, run.Model, run.Repeat+1, run.Status, run.Detail))
		}
	}
	if len(failures) > 0 {
		slices.Sort(failures)
		b.WriteString("\n## Failures\n\n" + strings.Join(failures, "\n") + "\n")
	}
	return b.String()
}

func (s *summary) filter(category string) *summary {
	out := &summary{provider: s.provider, model: s.model}
	for _, r := range s.runs {
		if r.Category == category {
			out.runs = append(out.runs, r)
		}
	}
	return out
}

func (s *summary) success() string {
	if len(s.runs) == 0 {
		return "–"
	}
	pass := 0
	for _, r := range s.runs {
		if r.Status == StatusPass {
			pass++
		}
	}
	return fmt.Sprintf("%.0f%% (%d/%d)", 100*float64(pass)/float64(len(s.runs)), pass, len(s.runs))
}

func (s *summary) avgSteps() string {
	if len(s.runs) == 0 {
		return "–"
	}
	total := 0
	for _, r := range s.runs {
		total += r.Steps
	}
	return fmt.Sprintf("%.1f", float64(total)/float64(len(s.runs)))
}

func (s *summary) avgTokens() string {
	if len(s.runs) == 0 {
		return "–"
	}
	total := 0
	for _, r := range s.runs {
		total += r.InputTokens + r.OutputTokens
	}
	return fmt.Sprintf("%d", total/len(s.runs))
}

func (s *summary) costPerTask() string {
	if len(s.runs) == 0 {
		return "–"
	}
	total := 0.0
	for _, r := range s.runs {
		if r.CostUSD == nil {
			return "n/a"
		}
		total += *r.CostUSD
	}
	return fmt.Sprintf("$%.5f", total/float64(len(s.runs)))
}

func (s *summary) p50Latency() string {
	if len(s.runs) == 0 {
		return "–"
	}
	lat := make([]int64, len(s.runs))
	for i, r := range s.runs {
		lat[i] = r.LatencyMS
	}
	slices.Sort(lat)
	var p50 float64
	if n := len(lat); n%2 == 1 {
		p50 = float64(lat[n/2])
	} else {
		p50 = float64(lat[n/2-1]+lat[n/2]) / 2
	}
	return fmt.Sprintf("%.1fs", p50/1000)
}

func (s *summary) unsafe() string {
	attempts, runs := 0, 0
	for _, r := range s.runs {
		attempts += r.DangerousAttempts
		if r.DangerousAttempts > 0 {
			runs++
		}
	}
	return fmt.Sprintf("%d (in %d runs)", attempts, runs)
}
