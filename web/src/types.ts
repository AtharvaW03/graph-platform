// Mirrors internal/query/types.go - keep field names in sync with the Go
// JSON tags, since this is a straight pass-through of the query-service API.

export interface RepoInfo {
  name: string;
  nodes: number;
}

export interface HotspotNode {
  name: string;
  repo: string;
  path: string;
  line: string;
  labels: string[];
  fan_in: number;
  dependent_repos: number;
}

export interface SearchResult {
  node_key: string;
  graphify_id: string;
  name: string;
  labels: string[];
  repo: string;
  path: string;
  line: string;
}

export interface SymbolResult {
  name: string;
  repo: string;
  path: string;
  line: string;
  labels: string[];
  community: number;
}

export interface CallEdge {
  caller: string;
  caller_repo: string;
  caller_path: string;
  caller_line: string;
  callee: string;
  callee_repo: string;
  callee_path: string;
  labels: string[];
}

export interface ImpactNode {
  name: string;
  repo: string;
  path: string;
  line: string;
  labels: string[];
  distance: number;
}

export interface PathNode {
  name: string;
  repo: string;
  path: string;
  labels: string[];
  relationship?: string;
}

export interface DependencyEdge {
  repo: string;
  name: string;
  labels: string[];
  ecosystem: string;
  version?: string;
  scope?: string;
  cross_repo: boolean;
}

export interface HTTPRoute {
  repo: string;
  method: string;
  path: string;
  handler?: string;
  labels: string[];
  file: string;
  line: string;
}

export interface KafkaTopicInfo {
  topic: string;
  producers: string[];
  consumers: string[];
}

export interface SQLObjectInfo {
  name: string;
  schema: string;
  kind: string;
  labels: string[];
  file?: string;
  line?: string;
  reads?: string[];
  writes?: string[];
  depends_on?: string[];
  triggers_on?: string;
}

export interface GlueJobInfo {
  name: string;
  repo: string;
  labels: string[];
  script?: string;
  schedule?: string;
  sources?: string[];
  targets?: string[];
  file?: string;
}

export interface LabeledCount {
  name: string;
  count: number;
}

export interface CommunitySummary {
  id: number;
  size: number;
  label: string;
  dominant_dir?: string;
  sample_members: string[];
}

export interface EntryPoint {
  name: string;
  kind: string;
  path: string;
  line: string;
  labels: string[];
}

export interface ModuleInfo {
  package: string;
  node_count: number;
  functions: number;
}

export interface RouteGroup {
  prefix: string;
  count: number;
  methods: string[];
}

export interface HTTPAPISummary {
  route_count: number;
  methods: LabeledCount[];
  groups: RouteGroup[];
}

export interface KafkaSummary {
  topics: string[];
  producers: string[];
  consumers: string[];
  by_topic?: KafkaTopicInfo[];
}

export interface SQLSummary {
  schemas: string[];
  tables: string[];
  views: string[];
  procedures: string[];
  functions: string[];
  triggers: string[];
}

export interface DependencySummary {
  internal_repos: string[];
  external: DependencyEdge[];
  top_ecosystems: LabeledCount[];
}

export interface ComponentInfo {
  name: string;
  path: string;
  degree: number;
  community: number;
  labels: string[];
}

export interface ReadingStep {
  category: string;
  why: string;
  items: string[];
}

export interface RepoMetadata {
  name: string;
  node_count: number;
  relationship_count: number;
  languages: LabeledCount[];
  node_kinds: LabeledCount[];
}

export interface ArchitectureInfo {
  summary: string;
  communities: CommunitySummary[];
}

export interface RepositoryOverview {
  repository: RepoMetadata;
  architecture: ArchitectureInfo;
  entry_points: EntryPoint[];
  modules: ModuleInfo[];
  http_apis: HTTPAPISummary;
  kafka: KafkaSummary;
  sql: SQLSummary;
  dependencies: DependencySummary;
  important_components: ComponentInfo[];
  suggested_reading_order: ReadingStep[];
}
