package extract

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// Runner executes a set of Extractors for one repo. Extractors run
// concurrently (no shared state), each with panic recovery so one bad plugin
// can't sink the whole pass.
type Runner struct {
	Extractors []Extractor
	Log        *log.Logger
	// MaxParallel caps concurrent extractors. Zero or negative means all.
	MaxParallel int
}

// Result aggregates one repo's extractor run. Errors are keyed per-extractor
// so the caller can tell an extractor failure from a sync/import failure.
type Result struct {
	Fragments []*Fragment
	Errors    map[string]error
	Warnings  map[string][]string
}

// Run executes every extractor against repoPath and gathers their fragments.
// A failing extractor never aborts the others; its error lands in Result.Errors.
func (r *Runner) Run(ctx context.Context, repoPath, repoName string) Result {
	res := Result{
		Errors:   map[string]error{},
		Warnings: map[string][]string{},
	}
	if len(r.Extractors) == 0 {
		return res
	}

	type partial struct {
		frag *Fragment
		err  error
		name string
	}
	results := make(chan partial, len(r.Extractors))

	parallel := r.MaxParallel
	if parallel <= 0 {
		parallel = len(r.Extractors)
	}
	sem := make(chan struct{}, parallel)

	var wg sync.WaitGroup
	for _, ex := range r.Extractors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			defer func() {
				if p := recover(); p != nil {
					results <- partial{name: ex.Name(), err: fmt.Errorf("panic: %v", p)}
				}
			}()

			frag, err := ex.Extract(ctx, repoPath, repoName)
			if err != nil {
				results <- partial{name: ex.Name(), err: err}
				return
			}
			if frag != nil {
				if vErr := frag.Validate(); vErr != nil {
					results <- partial{name: ex.Name(), err: fmt.Errorf("validate: %w", vErr)}
					return
				}
				frag.Extractor = ex.Name()
			}
			results <- partial{name: ex.Name(), frag: frag}
		}()
	}

	wg.Wait()
	close(results)

	for p := range results {
		if p.err != nil {
			res.Errors[p.name] = p.err
			if r.Log != nil {
				r.Log.Printf("extractor %q failed: %v", p.name, p.err)
			}
			continue
		}
		if p.frag == nil || p.frag.Empty() {
			continue
		}
		if len(p.frag.Warnings) > 0 {
			res.Warnings[p.name] = p.frag.Warnings
			if r.Log != nil {
				for _, w := range p.frag.Warnings {
					r.Log.Printf("extractor %q warning: %s", p.name, w)
				}
			}
		}
		res.Fragments = append(res.Fragments, p.frag)
	}
	return res
}
