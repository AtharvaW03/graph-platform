package query

import (
	"context"
	"fmt"
	"strings"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Result-set caps for the extractor-backed queries, which aggregate per-row
// collections.
const (
	depLimit     = 1000
	sqlLimit     = 200
	glueJobLimit = 500
)

// FindDependencies returns every package (and cross-repo target) the repo
// depends on. scope="" includes every scope; pass a scope to filter. The
// anchor is the deps extractor's per-repo hub Entity (graphify_id
// "repo::<name>"), not the (:Repository) node.
func (s *Service) FindDependencies(ctx context.Context, repo, scope string) ([]DependencyEdge, error) {
	if repo == "" {
		return []DependencyEdge{}, nil
	}
	const cypher = `
MATCH (r:Entity {graphify_id: 'repo::' + $repo})-[d:DEPENDS_ON|DEPENDS_ON_REPO]->(p:Entity)
WHERE $scope = '' OR d.context = $scope
RETURN p.name                          AS name,
       labels(p)                       AS labels,
       coalesce(p.ecosystem, '')        AS ecosystem,
       coalesce(p.version, '')          AS version,
       coalesce(d.context, '')          AS scope,
       type(d) = 'DEPENDS_ON_REPO'      AS cross
ORDER BY ecosystem, name
LIMIT $limit
`
	return s.runDepQuery(ctx, cypher, map[string]any{"repo": repo, "scope": scope, "limit": depLimit}, repo)
}

// FindDependents returns every repository that depends on dep. dep may be a
// package name or, for an inferred cross-repo edge, a short repo name.
// Case-insensitive: package names come from many ecosystems' manifests
// verbatim (deps extractor), and NuGet/Maven-style PascalCase names would
// silently miss an exact case-sensitive match.
func (s *Service) FindDependents(ctx context.Context, dep string) ([]DependencyEdge, error) {
	if dep == "" {
		return []DependencyEdge{}, nil
	}
	const cypher = `
MATCH (r:Entity)-[d:DEPENDS_ON|DEPENDS_ON_REPO]->(p:Entity)
WHERE r.graphify_id STARTS WITH 'repo::' AND p.name_lower = $dep
RETURN r.name                          AS name,
       labels(r)                       AS labels,
       coalesce(p.ecosystem, '')        AS ecosystem,
       coalesce(p.version, '')          AS version,
       coalesce(d.context, '')          AS scope,
       type(d) = 'DEPENDS_ON_REPO'      AS cross
ORDER BY name
LIMIT $limit
`
	dep = strings.ToLower(strings.TrimSpace(dep))
	return s.runDepQuery(ctx, cypher, map[string]any{"dep": dep, "limit": depLimit}, "")
}

func (s *Service) runDepQuery(ctx context.Context, cypher string, params map[string]any, repoBound string) ([]DependencyEdge, error) {
	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, params)
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		edges := make([]DependencyEdge, 0, len(records))
		for _, r := range records {
			m := r.AsMap()
			edges = append(edges, DependencyEdge{
				Repo:      repoBound,
				Name:      asString(m["name"]),
				Labels:    asStringSlice(m["labels"]),
				Ecosystem: asString(m["ecosystem"]),
				Version:   asString(m["version"]),
				Scope:     asString(m["scope"]),
				Cross:     asBool(m["cross"]),
			})
		}
		return edges, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]DependencyEdge), nil
}

// FindRoutes returns HTTP routes matching the filters (empty means any),
// ordered by repo then path. Method and path are parsed from the node's name
// ("METHOD /path"); each route carries a repo property, so scoping is a direct
// filter.
func (s *Service) FindRoutes(ctx context.Context, method, pathContains string, repos []string) ([]HTTPRoute, error) {
	const cypher = `
MATCH (rt:HttpRoute)
WITH rt,
     coalesce(rt.name, '')                                                             AS full,
     CASE WHEN rt.name CONTAINS ' ' THEN split(rt.name, ' ')[0] ELSE '' END              AS method_part,
     CASE WHEN rt.name CONTAINS ' ' THEN substring(rt.name, size(split(rt.name,' ')[0]) + 1) ELSE coalesce(rt.name, '') END AS path_part
WHERE ($method = '' OR toUpper(method_part) = toUpper($method))
  AND ($path = '' OR toLower(path_part) CONTAINS toLower($path))
  AND (size($repos) = 0 OR rt.repo IN $repos)
RETURN coalesce(rt.repo, '')            AS repo,
       method_part                      AS method,
       path_part                        AS path,
       coalesce(rt.handler, '')         AS handler,
       labels(rt)                       AS labels,
       coalesce(rt.path, '')            AS file_path,
       coalesce(rt.line, '')            AS line,
       coalesce(rt.source, 'code')      AS source,
       coalesce(rt.documented, false)   AS documented,
       coalesce(rt.classification, '')  AS classification,
       coalesce(rt.tags, [])            AS tags
ORDER BY repo, path
LIMIT 500
`
	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, map[string]any{
			"method": method, "path": pathContains, "repos": orEmpty(repos),
		})
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]HTTPRoute, 0, len(records))
		for _, rec := range records {
			m := rec.AsMap()
			out = append(out, HTTPRoute{
				Repo:           asString(m["repo"]),
				Method:         asString(m["method"]),
				Path:           asString(m["path"]),
				Handler:        asString(m["handler"]),
				Labels:         asStringSlice(m["labels"]),
				File:           asString(m["file_path"]),
				Line:           asString(m["line"]),
				Source:         asString(m["source"]),
				Documented:     asBool(m["documented"]),
				Classification: asString(m["classification"]),
				Tags:           asStringSlice(m["tags"]),
			})
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]HTTPRoute), nil
}

// FindKafkaTopic returns one topic plus all repositories that produce to or
// consume from it. topic="" is an error. Case-insensitive: topic names are
// stored however the extractor found them (some repos declare topics in
// SCREAMING_SNAKE_CASE), so an exact-case match would silently miss real,
// indexed topics.
func (s *Service) FindKafkaTopic(ctx context.Context, topic string) (*KafkaTopicInfo, error) {
	if topic == "" {
		return nil, fmt.Errorf("topic required")
	}
	// Kafka extractor emits PRODUCES/CONSUMES from its per-repo hub Entity
	// (graphify_id = "repo::<name>"), not from :Repository. Filter to those
	// hub nodes so we surface repo names, not arbitrary function nodes if
	// the extractor is later extended to emit finer-grained producers.
	//
	// t.name is wrapped in its own collect(DISTINCT ...) alongside the
	// producer/consumer collects, not returned bare: a bare non-aggregated
	// column next to aggregates makes Cypher implicitly group by it, which
	// would silently split the result (and any caller's records[0] pick)
	// across a "legacy duplicate" or case-variant topic node instead of
	// merging them into the one answer this function promises.
	//
	// The "WITH collect(t) AS matches WHERE size(matches) > 0" step matters:
	// once every RETURN column is an aggregate, Cypher still emits one row
	// of nulls/empties for zero matches (an aggregate over zero rows is
	// still a row), which would silently turn "topic not found" into a
	// present-but-empty result instead of the nil this function's callers
	// rely on for a 404. This filters that case out before it can happen.
	const cypher = `
MATCH (t:KafkaTopic)
WHERE t.name_lower = $topic
WITH collect(t) AS matches
WHERE size(matches) > 0
UNWIND matches AS t
OPTIONAL MATCH (rp:Entity)-[:PRODUCES]->(t) WHERE rp.graphify_id STARTS WITH 'repo::'
OPTIONAL MATCH (rc:Entity)-[:CONSUMES]->(t) WHERE rc.graphify_id STARTS WITH 'repo::'
RETURN head(collect(DISTINCT t.name))     AS topic,
       collect(DISTINCT rp.name)          AS producers,
       collect(DISTINCT rc.name)          AS consumers
`
	topic = strings.ToLower(strings.TrimSpace(topic))
	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, map[string]any{"topic": topic})
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			return (*KafkaTopicInfo)(nil), nil
		}
		m := records[0].AsMap()
		info := &KafkaTopicInfo{
			Topic:     asString(m["topic"]),
			Producers: filterEmpty(asStringSlice(m["producers"])),
			Consumers: filterEmpty(asStringSlice(m["consumers"])),
		}
		return info, nil
	})
	if err != nil {
		return nil, err
	}
	return out.(*KafkaTopicInfo), nil
}

// FindSQLObject returns matching SQL Server objects (by bare or fully-qualified
// name) plus the tables they read, write, depend on, or trigger on.
// Case-insensitive: SQL Server's default collation is itself case-insensitive
// (SQL_Latin1_General_CP1_CI_AS and friends), so an exact-case Cypher match
// would diverge from how the objects actually behave in the database it's
// describing - "dbo.Orders" and "dbo.orders" are the same table to SQL
// Server, and this lookup needs to agree.
func (s *Service) FindSQLObject(ctx context.Context, schema, name string) ([]SQLObjectInfo, error) {
	schema = strings.ToLower(strings.TrimSpace(schema))
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return []SQLObjectInfo{}, nil
	}
	full := name
	if schema != "" {
		full = schema + "." + name
	}
	const cypher = `
MATCH (o:Entity)
WHERE any(l IN labels(o) WHERE l IN ['SqlTable','SqlView','SqlProcedure','SqlTrigger','SqlFunction','SqlSchema'])
  AND (
    o.name_lower = $full
    OR ($schema = '' AND split(coalesce(o.name_lower, ''), '.')[size(split(coalesce(o.name_lower, ''), '.'))-1] = $name)
  )
OPTIONAL MATCH (o)-[:READS_TABLE]->(rt:SqlTable)
OPTIONAL MATCH (o)-[:WRITES_TABLE]->(wt:SqlTable)
OPTIONAL MATCH (o)-[:DEPENDS_ON_OBJECT]->(dep:Entity)
OPTIONAL MATCH (o)-[:TRIGGERS_ON]->(tt:SqlTable)
RETURN o.name                              AS name,
       labels(o)                           AS labels,
       coalesce(o.path, '')                 AS file,
       coalesce(o.line, '')                 AS line,
       collect(DISTINCT rt.name)            AS reads,
       collect(DISTINCT wt.name)            AS writes,
       collect(DISTINCT dep.name)           AS depends_on,
       coalesce(head(collect(DISTINCT tt.name)), '') AS triggers_on
ORDER BY name
LIMIT $limit
`
	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, map[string]any{"schema": schema, "name": name, "full": full, "limit": sqlLimit})
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]SQLObjectInfo, 0, len(records))
		for _, rec := range records {
			m := rec.AsMap()
			labels := asStringSlice(m["labels"])
			kind := ""
			for _, l := range labels {
				switch l {
				case "SqlTable", "SqlView", "SqlProcedure", "SqlTrigger", "SqlFunction", "SqlSchema":
					kind = l
				}
			}
			fullName := asString(m["name"])
			sch, base := splitSchemaName(fullName)
			out = append(out, SQLObjectInfo{
				Name:       base,
				Schema:     sch,
				Kind:       kind,
				Labels:     labels,
				File:       asString(m["file"]),
				Line:       asString(m["line"]),
				Reads:      filterEmpty(asStringSlice(m["reads"])),
				Writes:     filterEmpty(asStringSlice(m["writes"])),
				DependsOn:  filterEmpty(asStringSlice(m["depends_on"])),
				TriggersOn: asString(m["triggers_on"]),
			})
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]SQLObjectInfo), nil
}

// FindGlueJobs returns Glue jobs filtered by source or destination table.
// Pass both arguments empty to list every Glue job. Case-insensitive source/
// target matching, same reasoning as FindSQLObject: these are frequently the
// same SQL Server tables, whose real-world identity is case-insensitive.
func (s *Service) FindGlueJobs(ctx context.Context, source, target string) ([]GlueJobInfo, error) {
	const cypher = `
MATCH (j:GlueJob)
OPTIONAL MATCH (j)-[:READS_SOURCE]->(s:Entity)
OPTIONAL MATCH (j)-[:WRITES_DESTINATION]->(t:Entity)
OPTIONAL MATCH (r:Repository)-[:HAS_ENTITY]->(j)
WITH j, r, collect(DISTINCT s.name) AS sources, collect(DISTINCT t.name) AS targets
WHERE ($source = '' OR $source IN [x IN sources | toLower(x)])
  AND ($target = '' OR $target IN [x IN targets | toLower(x)])
RETURN j.name                AS name,
       coalesce(r.name, '')   AS repo,
       labels(j)              AS labels,
       coalesce(j.path, '')    AS file,
       coalesce(j.script, '')  AS script,
       coalesce(j.schedule, '') AS schedule,
       sources                AS sources,
       targets                AS targets
ORDER BY repo, name
LIMIT $limit
`
	source = strings.ToLower(strings.TrimSpace(source))
	target = strings.ToLower(strings.TrimSpace(target))
	out, err := s.read(ctx, func(tx driver.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, map[string]any{"source": source, "target": target, "limit": glueJobLimit})
		if err != nil {
			return nil, err
		}
		records, err := res.Collect(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]GlueJobInfo, 0, len(records))
		for _, rec := range records {
			m := rec.AsMap()
			out = append(out, GlueJobInfo{
				Name:     asString(m["name"]),
				Repo:     asString(m["repo"]),
				Labels:   asStringSlice(m["labels"]),
				File:     asString(m["file"]),
				Script:   asString(m["script"]),
				Schedule: asString(m["schedule"]),
				Sources:  filterEmpty(asStringSlice(m["sources"])),
				Targets:  filterEmpty(asStringSlice(m["targets"])),
			})
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]GlueJobInfo), nil
}

func splitSchemaName(full string) (schema, name string) {
	for i := 0; i < len(full); i++ {
		if full[i] == '.' {
			return full[:i], full[i+1:]
		}
	}
	return "", full
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func filterEmpty(xs []string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}
