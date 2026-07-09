package query

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Result caps that keep the overview a single compact payload.
const (
	overviewCommunities  = 12
	overviewModules      = 30
	overviewHubs         = 15
	overviewExternalDeps = 100
	overviewReadingItems = 8
)

// RepositoryOverview aggregates the indexed graph for one repository into a
// single onboarding snapshot, built entirely from Neo4j (no source access).
func (s *Service) RepositoryOverview(ctx context.Context, repo string) (*RepositoryOverview, error) {
	if repo == "" {
		return nil, fmt.Errorf("repo required")
	}

	// Fail fast if the repo isn't indexed rather than return a hollow overview.
	nodeCount, relCount, err := s.overviewCounts(ctx, repo)
	if err != nil {
		return nil, err
	}
	if nodeCount == 0 {
		return nil, fmt.Errorf("repository %q has no indexed nodes", repo)
	}

	// The remaining reads are independent, so run them concurrently.
	var (
		kinds       []LabeledCount
		langs       []LabeledCount
		communities []CommunitySummary
		entryPoints []EntryPoint
		modules     []ModuleInfo
		hubs        []ComponentInfo
		kafka       KafkaSummary
		sqlSummary  SQLSummary
		routes      []HTTPRoute
		deps        []DependencyEdge
	)
	if err := parallelReads(
		func() (e error) { kinds, e = s.overviewLabelCounts(ctx, repo); return },
		func() (e error) { langs, e = s.overviewLanguages(ctx, repo); return },
		func() (e error) { communities, e = s.overviewCommunities(ctx, repo); return },
		func() (e error) { entryPoints, e = s.overviewEntryPoints(ctx, repo); return },
		func() (e error) { modules, e = s.overviewModules(ctx, repo); return },
		func() (e error) { hubs, e = s.overviewHubs(ctx, repo); return },
		func() (e error) { kafka, e = s.overviewKafka(ctx, repo); return },
		func() (e error) { sqlSummary, e = s.overviewSQL(ctx, repo); return },
		func() (e error) { routes, e = s.FindRoutes(ctx, "", "", []string{repo}); return },
		func() (e error) { deps, e = s.FindDependencies(ctx, repo, ""); return },
	); err != nil {
		return nil, err
	}

	httpAPIs := summarizeRoutes(routes)
	dependencies := summarizeDependencies(deps)

	ov := &RepositoryOverview{
		Repository: RepoMetadata{
			Name:              repo,
			NodeCount:         nodeCount,
			RelationshipCount: relCount,
			Languages:         langs,
			NodeKinds:         kinds,
		},
		Architecture: ArchitectureInfo{
			Communities: communities,
		},
		EntryPoints:  entryPoints,
		Modules:      modules,
		HTTPAPIs:     httpAPIs,
		Kafka:        kafka,
		SQL:          sqlSummary,
		Dependencies: dependencies,
		Components:   hubs,
	}
	ov.Architecture.Summary = synthesizeSummary(ov)
	ov.ReadingOrder = buildReadingOrder(ov)
	ov.normalize()
	return ov, nil
}

// parallelReads runs independent read closures concurrently and joins their
// errors. Each closure gets its own session; panics are recovered so one bad
// read can't crash the server.
func parallelReads(fns ...func() error) error {
	var wg sync.WaitGroup
	errs := make([]error, len(fns))
	for i, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("overview read panicked: %v", r)
				}
			}()
			errs[i] = fn()
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// normalize replaces nil slices with empty ones so list fields serialize as
// [] rather than null.
func (ov *RepositoryOverview) normalize() {
	ov.Repository.Languages = orEmptyCounts(ov.Repository.Languages)
	ov.Repository.NodeKinds = orEmptyCounts(ov.Repository.NodeKinds)
	if ov.Architecture.Communities == nil {
		ov.Architecture.Communities = []CommunitySummary{}
	}
	if ov.EntryPoints == nil {
		ov.EntryPoints = []EntryPoint{}
	}
	if ov.Modules == nil {
		ov.Modules = []ModuleInfo{}
	}
	if ov.Components == nil {
		ov.Components = []ComponentInfo{}
	}
	if ov.ReadingOrder == nil {
		ov.ReadingOrder = []ReadingStep{}
	}
	ov.HTTPAPIs.Methods = orEmptyCounts(ov.HTTPAPIs.Methods)
	if ov.HTTPAPIs.Groups == nil {
		ov.HTTPAPIs.Groups = []RouteGroup{}
	}
	ov.Kafka.Topics = orEmptyStrings(ov.Kafka.Topics)
	ov.Kafka.Producers = orEmptyStrings(ov.Kafka.Producers)
	ov.Kafka.Consumers = orEmptyStrings(ov.Kafka.Consumers)
	if ov.Kafka.ByTopic == nil {
		ov.Kafka.ByTopic = []KafkaTopicInfo{}
	}
	ov.SQL.Schemas = orEmptyStrings(ov.SQL.Schemas)
	ov.SQL.Tables = orEmptyStrings(ov.SQL.Tables)
	ov.SQL.Views = orEmptyStrings(ov.SQL.Views)
	ov.SQL.Procedures = orEmptyStrings(ov.SQL.Procedures)
	ov.SQL.Functions = orEmptyStrings(ov.SQL.Functions)
	ov.SQL.Triggers = orEmptyStrings(ov.SQL.Triggers)
	ov.Dependencies.InternalRepos = orEmptyStrings(ov.Dependencies.InternalRepos)
	ov.Dependencies.TopEcosystems = orEmptyCounts(ov.Dependencies.TopEcosystems)
	if ov.Dependencies.External == nil {
		ov.Dependencies.External = []DependencyEdge{}
	}
}

func orEmptyStrings(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func orEmptyCounts(xs []LabeledCount) []LabeledCount {
	if xs == nil {
		return []LabeledCount{}
	}
	return xs
}

// each runs cypher and invokes fn once per result row.
func (s *Service) each(ctx context.Context, cypher string, params map[string]any, fn func(m map[string]any)) error {
	_, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, params)
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range records {
			fn(r.AsMap())
		}
		return nil, nil
	})
	return err
}

// overviewCounts returns the repo's node count and its internal relationship
// count in a single query.
func (s *Service) overviewCounts(ctx context.Context, repo string) (nodes, rels int, err error) {
	const cypher = `
MATCH (n:Entity {repo: $repo})
OPTIONAL MATCH (n)-[r]->(:Entity {repo: $repo})
RETURN count(DISTINCT n) AS nodes, count(r) AS rels
`
	err = s.each(ctx, cypher, map[string]any{"repo": repo}, func(m map[string]any) {
		nodes = asInt(m["nodes"])
		rels = asInt(m["rels"])
	})
	return nodes, rels, err
}

func (s *Service) overviewLabelCounts(ctx context.Context, repo string) ([]LabeledCount, error) {
	// Scoped via CONTAINS so shared entities (topics, packages, SQL) count too.
	const cypher = `
MATCH (:Repository {name: $repo})-[:CONTAINS]->(n:Entity)
UNWIND labels(n) AS l
WITH l WHERE l <> 'Entity'
RETURN l AS name, count(*) AS c
ORDER BY c DESC, name
`
	var out []LabeledCount
	err := s.each(ctx, cypher, map[string]any{"repo": repo}, func(m map[string]any) {
		out = append(out, LabeledCount{Name: asString(m["name"]), Count: asInt(m["c"])})
	})
	return out, err
}

func (s *Service) overviewLanguages(ctx context.Context, repo string) ([]LabeledCount, error) {
	const cypher = `
MATCH (n:Entity {repo: $repo})
WHERE coalesce(n.language, '') <> ''
RETURN n.language AS name, count(*) AS c
ORDER BY c DESC, name
`
	var out []LabeledCount
	err := s.each(ctx, cypher, map[string]any{"repo": repo}, func(m map[string]any) {
		out = append(out, LabeledCount{Name: asString(m["name"]), Count: asInt(m["c"])})
	})
	return out, err
}

// overviewCommunities returns the largest communities with sample members.
// Label and DominantDir are synthesized from member paths (community_name is
// empty at import).
func (s *Service) overviewCommunities(ctx context.Context, repo string) ([]CommunitySummary, error) {
	const cypher = `
MATCH (n:Entity {repo: $repo})
WHERE n.community IS NOT NULL
WITH n.community AS community, collect(n) AS ns
WITH community, size(ns) AS sz, ns
ORDER BY sz DESC
LIMIT $limit
UNWIND ns AS n
RETURN community AS community,
       sz        AS size,
       collect(n.name)[0..8]              AS names,
       collect(coalesce(n.path, ''))[0..50] AS paths
ORDER BY size DESC
`
	var out []CommunitySummary
	err := s.each(ctx, cypher, map[string]any{"repo": repo, "limit": overviewCommunities}, func(m map[string]any) {
		dir := dominantDir(asStringSlice(m["paths"]))
		out = append(out, CommunitySummary{
			ID:            asInt(m["community"]),
			Size:          asInt(m["size"]),
			DominantDir:   dir,
			Label:         communityLabel(dir, asStringSlice(m["names"])),
			SampleMembers: filterEmpty(asStringSlice(m["names"])),
		})
	})
	return out, err
}

// overviewEntryPoints returns executable mains, servers, and bootstrap funcs.
func (s *Service) overviewEntryPoints(ctx context.Context, repo string) ([]EntryPoint, error) {
	// name_lower is pre-lowercased at import, so no per-row toLower.
	const cypher = `
MATCH (n:Entity {repo: $repo})
WHERE 'Function' IN labels(n) AND (
      n.name_lower = 'main()'
   OR n.name_lower CONTAINS 'newserver'
   OR n.name_lower CONTAINS 'listenandserve'
   OR n.name_lower CONTAINS 'runserver'
   OR n.name_lower STARTS WITH 'serve'
   OR n.name_lower STARTS WITH 'start'
   OR n.name_lower STARTS WITH 'bootstrap'
   OR n.name_lower STARTS WITH 'run('
)
RETURN n.name AS name,
       coalesce(n.path, '') AS path,
       coalesce(n.line, '') AS line,
       labels(n) AS labels
ORDER BY path, name
LIMIT 100
`
	var out []EntryPoint
	err := s.each(ctx, cypher, map[string]any{"repo": repo}, func(m map[string]any) {
		name := asString(m["name"])
		out = append(out, EntryPoint{
			Name:   name,
			Kind:   classifyEntryPoint(name),
			Path:   asString(m["path"]),
			Line:   asString(m["line"]),
			Labels: asStringSlice(m["labels"]),
		})
	})
	if err != nil {
		return nil, err
	}
	// Executable mains first, then servers, then bootstrap.
	sort.SliceStable(out, func(i, j int) bool {
		return entryKindRank(out[i].Kind) < entryKindRank(out[j].Kind)
	})
	return out, nil
}

// overviewModules groups nodes by containing directory with node and function
// counts.
func (s *Service) overviewModules(ctx context.Context, repo string) ([]ModuleInfo, error) {
	const cypher = `
MATCH (n:Entity {repo: $repo})
WHERE coalesce(n.path, '') <> ''
WITH n, split(n.path, '/') AS parts
WITH n, CASE WHEN size(parts) <= 1 THEN '.'
        ELSE reduce(s = '', i IN range(0, size(parts) - 2) |
             s + CASE WHEN s = '' THEN '' ELSE '/' END + parts[i]) END AS dir
RETURN dir AS package,
       count(*) AS nodes,
       sum(CASE WHEN 'Function' IN labels(n) THEN 1 ELSE 0 END) AS functions
ORDER BY nodes DESC, package
LIMIT $limit
`
	var out []ModuleInfo
	err := s.each(ctx, cypher, map[string]any{"repo": repo, "limit": overviewModules}, func(m map[string]any) {
		out = append(out, ModuleInfo{
			Package:   asString(m["package"]),
			NodeCount: asInt(m["nodes"]),
			Functions: asInt(m["functions"]),
		})
	})
	return out, err
}

// overviewHubs returns the highest-degree nodes (the core abstractions).
func (s *Service) overviewHubs(ctx context.Context, repo string) ([]ComponentInfo, error) {
	const cypher = `
MATCH (n:Entity {repo: $repo})
WITH n, COUNT { (n)--() } AS degree
RETURN n.name AS name,
       coalesce(n.path, '') AS path,
       labels(n) AS labels,
       coalesce(n.community, -1) AS community,
       degree
ORDER BY degree DESC, name
LIMIT $limit
`
	var out []ComponentInfo
	err := s.each(ctx, cypher, map[string]any{"repo": repo, "limit": overviewHubs}, func(m map[string]any) {
		out = append(out, ComponentInfo{
			Name:      asString(m["name"]),
			Path:      asString(m["path"]),
			Degree:    asInt(m["degree"]),
			Community: asInt(m["community"]),
			Labels:    asStringSlice(m["labels"]),
		})
	})
	return out, err
}

// overviewKafka returns the topics the repo references with their producers
// and consumers. Topics are shared nodes, scoped via CONTAINS; the producer
// and consumer lists span all repos to show the full topology.
func (s *Service) overviewKafka(ctx context.Context, repo string) (KafkaSummary, error) {
	const cypher = `
MATCH (:Repository {name: $repo})-[:CONTAINS]->(t:KafkaTopic)
OPTIONAL MATCH (p:Entity)-[:PRODUCES]->(t)
OPTIONAL MATCH (c:Entity)-[:CONSUMES]->(t)
RETURN t.name AS topic,
       collect(DISTINCT p.name) AS producers,
       collect(DISTINCT c.name) AS consumers
ORDER BY topic
`
	var summary KafkaSummary
	producers := map[string]bool{}
	consumers := map[string]bool{}
	err := s.each(ctx, cypher, map[string]any{"repo": repo}, func(m map[string]any) {
		topic := asString(m["topic"])
		ps := filterEmpty(asStringSlice(m["producers"]))
		cs := filterEmpty(asStringSlice(m["consumers"]))
		summary.Topics = append(summary.Topics, topic)
		summary.ByTopic = append(summary.ByTopic, KafkaTopicInfo{Topic: topic, Producers: ps, Consumers: cs})
		for _, p := range ps {
			producers[p] = true
		}
		for _, c := range cs {
			consumers[c] = true
		}
	})
	if err != nil {
		return KafkaSummary{}, err
	}
	summary.Producers = sortedKeys(producers)
	summary.Consumers = sortedKeys(consumers)
	return summary, nil
}

// overviewSQL groups the SQL objects the repo references by kind (scoped via
// CONTAINS, since SQL objects are shared nodes).
func (s *Service) overviewSQL(ctx context.Context, repo string) (SQLSummary, error) {
	const cypher = `
MATCH (:Repository {name: $repo})-[:CONTAINS]->(o:Entity)
WHERE any(l IN labels(o) WHERE l IN
      ['SqlSchema','SqlTable','SqlView','SqlProcedure','SqlFunction','SqlTrigger'])
RETURN DISTINCT head([l IN labels(o) WHERE l STARTS WITH 'Sql']) AS kind,
       o.name AS name
ORDER BY kind, name
`
	var summary SQLSummary
	err := s.each(ctx, cypher, map[string]any{"repo": repo}, func(m map[string]any) {
		name := asString(m["name"])
		switch asString(m["kind"]) {
		case "SqlSchema":
			summary.Schemas = append(summary.Schemas, name)
		case "SqlTable":
			summary.Tables = append(summary.Tables, name)
		case "SqlView":
			summary.Views = append(summary.Views, name)
		case "SqlProcedure":
			summary.Procedures = append(summary.Procedures, name)
		case "SqlFunction":
			summary.Functions = append(summary.Functions, name)
		case "SqlTrigger":
			summary.Triggers = append(summary.Triggers, name)
		}
	})
	return summary, err
}

// --- aggregation helpers ---

// summarizeRoutes turns the flat route inventory into a method distribution
// and prefix-grouped buckets.
func summarizeRoutes(routes []HTTPRoute) HTTPAPISummary {
	methods := map[string]int{}
	groupCounts := map[string]int{}
	groupMethods := map[string]map[string]bool{}
	for _, rt := range routes {
		if rt.Method != "" {
			methods[rt.Method]++
		}
		prefix := routePrefix(rt.Path)
		groupCounts[prefix]++
		if groupMethods[prefix] == nil {
			groupMethods[prefix] = map[string]bool{}
		}
		if rt.Method != "" {
			groupMethods[prefix][rt.Method] = true
		}
	}
	groups := make([]RouteGroup, 0, len(groupCounts))
	for prefix, count := range groupCounts {
		groups = append(groups, RouteGroup{
			Prefix:  prefix,
			Count:   count,
			Methods: sortedKeys(groupMethods[prefix]),
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Count != groups[j].Count {
			return groups[i].Count > groups[j].Count
		}
		return groups[i].Prefix < groups[j].Prefix
	})
	return HTTPAPISummary{
		RouteCount: len(routes),
		Methods:    countsToSorted(methods),
		Groups:     groups,
	}
}

// summarizeDependencies splits the dependency edges into cross-repo internal
// targets and external packages, and ranks the ecosystems.
func summarizeDependencies(deps []DependencyEdge) DependencySummary {
	var summary DependencySummary
	internal := map[string]bool{}
	ecosystems := map[string]int{}
	for _, d := range deps {
		if d.Cross {
			internal[d.Name] = true
			continue
		}
		if len(summary.External) < overviewExternalDeps {
			summary.External = append(summary.External, d)
		}
		if d.Ecosystem != "" {
			ecosystems[d.Ecosystem]++
		}
	}
	summary.InternalRepos = sortedKeys(internal)
	summary.TopEcosystems = countsToSorted(ecosystems)
	return summary
}

// synthesizeSummary composes a one-line description from the aggregated
// metrics, using node-kind counts rather than the sparsely-populated language
// field.
func synthesizeSummary(ov *RepositoryOverview) string {
	parts := []string{
		fmt.Sprintf("%s: %d nodes, %d relationships, %d communities",
			ov.Repository.Name, ov.Repository.NodeCount, ov.Repository.RelationshipCount, len(ov.Architecture.Communities)),
	}
	if kinds := topKindsPhrase(ov.Repository.NodeKinds, 2); kinds != "" {
		parts = append(parts, kinds)
	}
	if ov.HTTPAPIs.RouteCount > 0 {
		parts = append(parts, fmt.Sprintf("%d HTTP routes", ov.HTTPAPIs.RouteCount))
	}
	if len(ov.Kafka.Topics) > 0 {
		parts = append(parts, fmt.Sprintf("%d Kafka topics", len(ov.Kafka.Topics)))
	}
	if n := len(ov.SQL.Tables) + len(ov.SQL.Procedures) + len(ov.SQL.Views) + len(ov.SQL.Functions); n > 0 {
		parts = append(parts, fmt.Sprintf("%d SQL objects", n))
	}
	if len(ov.EntryPoints) > 0 {
		parts = append(parts, fmt.Sprintf("%d entry points", len(ov.EntryPoints)))
	}
	return strings.Join(parts, ", ") + "."
}

// topKindsPhrase renders the top n node kinds as "1475 Function, 613 Class".
func topKindsPhrase(kinds []LabeledCount, n int) string {
	parts := make([]string, 0, n)
	for _, k := range kinds {
		if len(parts) == n {
			break
		}
		parts = append(parts, fmt.Sprintf("%d %s", k.Count, k.Name))
	}
	return strings.Join(parts, ", ")
}

// buildReadingOrder derives an onboarding path: entry points, core packages,
// services, infrastructure, then utilities.
func buildReadingOrder(ov *RepositoryOverview) []ReadingStep {
	var steps []ReadingStep

	// 1. Entry points - where execution begins.
	if items := entryPointItems(ov.EntryPoints); len(items) > 0 {
		steps = append(steps, ReadingStep{
			Category: "entry_points",
			Why:      "where each process starts - trace startup from here",
			Items:    capStrings(items, overviewReadingItems),
		})
	}

	// Bucket modules by role using directory-name heuristics.
	var core, infra, util []string
	for _, mod := range ov.Modules {
		if isNoiseModule(mod.Package) {
			continue
		}
		switch classifyModule(mod.Package) {
		case "infrastructure":
			infra = append(infra, mod.Package)
		case "utility":
			util = append(util, mod.Package)
		default:
			core = append(core, mod.Package)
		}
	}

	// 2. Core packages - the largest domain modules.
	if len(core) > 0 {
		steps = append(steps, ReadingStep{
			Category: "core_packages",
			Why:      "largest domain modules by node count - the bulk of the logic",
			Items:    capStrings(core, overviewReadingItems),
		})
	}

	// 3. Services - the external surface (HTTP, Kafka, SQL).
	if items := serviceItems(ov); len(items) > 0 {
		steps = append(steps, ReadingStep{
			Category: "services",
			Why:      "external surface area: HTTP routes, Kafka topics, SQL objects",
			Items:    capStrings(items, overviewReadingItems),
		})
	}

	// 4. Infrastructure - config, persistence, transport wiring.
	if len(infra) > 0 {
		steps = append(steps, ReadingStep{
			Category: "infrastructure",
			Why:      "config, persistence, and transport wiring that supports the services",
			Items:    capStrings(infra, overviewReadingItems),
		})
	}

	// 5. Utilities - read last, mostly on demand.
	if len(util) > 0 {
		steps = append(steps, ReadingStep{
			Category: "utilities",
			Why:      "shared helpers and generated code - read on demand",
			Items:    capStrings(util, overviewReadingItems),
		})
	}

	return steps
}

func entryPointItems(eps []EntryPoint) []string {
	out := make([]string, 0, len(eps))
	for _, ep := range eps {
		out = append(out, ep.Name+" @ "+ep.Path)
	}
	return out
}

// serviceItems lists the external surface: top route groups, then topics,
// then SQL tables.
func serviceItems(ov *RepositoryOverview) []string {
	var out []string
	for _, g := range ov.HTTPAPIs.Groups {
		out = append(out, fmt.Sprintf("routes %s (%d)", g.Prefix, g.Count))
	}
	for _, t := range ov.Kafka.Topics {
		out = append(out, "topic "+t)
	}
	for _, t := range ov.SQL.Tables {
		out = append(out, "table "+t)
	}
	return out
}

// --- helpers ---

// dominantDir returns the most common directory among a set of file paths.
func dominantDir(paths []string) string {
	counts := map[string]int{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		counts[dirOf(p)]++
	}
	best, bestN := "", 0
	for dir, n := range counts {
		if n > bestN || (n == bestN && dir < best) {
			best, bestN = dir, n
		}
	}
	return best
}

func dirOf(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return "."
	}
	return path[:i]
}

// communityLabel derives a community name from its dominant directory, falling
// back to a member name.
func communityLabel(dir string, names []string) string {
	if dir != "" && dir != "." {
		return dir
	}
	for _, n := range names {
		if n != "" {
			return n
		}
	}
	return "misc"
}

func classifyEntryPoint(name string) string {
	l := strings.ToLower(name)
	switch {
	case l == "main()":
		return "executable_main"
	case strings.Contains(l, "server") || strings.Contains(l, "listenandserve") || strings.HasPrefix(l, "serve"):
		return "server"
	default:
		return "bootstrap"
	}
}

func entryKindRank(kind string) int {
	switch kind {
	case "executable_main":
		return 0
	case "server":
		return 1
	default:
		return 2
	}
}

// routePrefix returns a stable grouping key for an HTTP path - its first two
// segments (e.g. "/api/v1/users" -> "/api/v1"), or "/" for root.
func routePrefix(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return "/"
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 1 {
		return "/" + parts[0]
	}
	return "/" + parts[0] + "/" + parts[1]
}

// classifyModule buckets a package/directory by role using name heuristics.
func classifyModule(pkg string) string {
	l := strings.ToLower(pkg)
	infraKeys := []string{"config", "postgres", "/db", "database", "kafka", "server", "middleware", "neo4j", "redis", "cache", "jwt", "auth", "queue", "broker", "transport"}
	for _, k := range infraKeys {
		if strings.Contains(l, k) {
			return "infrastructure"
		}
	}
	utilKeys := []string{"util", "helper", "common", "internal/tools", "generated", "graphql_models", "mock", "testutil"}
	for _, k := range utilKeys {
		if strings.Contains(l, k) {
			return "utility"
		}
	}
	return "core"
}

// isNoiseModule filters directories that add little onboarding value.
func isNoiseModule(pkg string) bool {
	l := strings.ToLower(pkg)
	for _, k := range []string{"vendor/", "/vendor", "node_modules", "testdata"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// countsToSorted turns a name→count map into a slice ordered by count desc,
// then name.
func countsToSorted(m map[string]int) []LabeledCount {
	out := make([]LabeledCount, 0, len(m))
	for name, c := range m {
		out = append(out, LabeledCount{Name: name, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// sortedKeys returns the true keys of a set as a sorted slice.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func capStrings(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return xs[:n]
}
