// Package kafka extracts Kafka topology (topics, producers, consumers) from a
// repository. It scans source for client-library call patterns per language
// and YAML config for declared topic names, since services usually read topic
// names from config rather than inlining them at the call site.
//
// It emits one KafkaTopic node per topic plus PRODUCES/CONSUMES edges from the
// repo hub; topics with no direction hint get a REFERENCES edge. Every edge is
// INFERRED.
package kafka

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"a1-knowledge-graph/internal/extract"
)

type Extractor struct {
	MaxFileBytes int64
}

func New() *Extractor { return &Extractor{MaxFileBytes: 2 * 1024 * 1024} }

func (e *Extractor) Name() string { return "kafka" }

// patternSet groups the regexes for one language. Every pattern must capture
// the topic name; a match without one emits nothing.
type patternSet struct {
	produces []*regexp.Regexp
	consumes []*regexp.Regexp
}

var (
	// Go: sarama, segmentio/kafka-go, confluent-kafka-go. Confluent producing
	// uses a pointer topic that regex can't resolve; those come from config.
	goPatterns = patternSet{
		produces: []*regexp.Regexp{
			regexp.MustCompile(`ProducerMessage\s*\{[^}]*Topic\s*:\s*"([^"]+)"`),
			regexp.MustCompile(`Writer\s*\{[^}]*Topic\s*:\s*"([^"]+)"`),
		},
		consumes: []*regexp.Regexp{
			// Matches both ReaderConfig{} and bare Reader{}.
			regexp.MustCompile(`Reader(?:Config)?\s*\{[^}]*Topic\s*:\s*"([^"]+)"`),
			regexp.MustCompile(`ConsumeClaim\s*\([^)]*"([^"]+)"`),
			// Bare .Subscribe( also matches other pub-sub clients; fine since
			// edges are INFERRED.
			regexp.MustCompile(`\.Subscribe\s*\(\s*"([^"]+)"`),
			// Only the first literal in the topic list is captured.
			regexp.MustCompile(`\.SubscribeTopics\s*\(\s*\[\]string\s*\{\s*"([^"]+)"`),
		},
	}
	// Java/Kotlin/Scala: KafkaProducer/KafkaConsumer, Spring Kafka @KafkaListener.
	jvmPatterns = patternSet{
		produces: []*regexp.Regexp{
			regexp.MustCompile(`\.send\s*\(\s*new\s+ProducerRecord\s*<[^>]*>\s*\(\s*"([^"]+)"`),
			regexp.MustCompile(`\.send\s*\(\s*"([^"]+)"`),
		},
		consumes: []*regexp.Regexp{
			regexp.MustCompile(`@KafkaListener\s*\(\s*topics?\s*=\s*[\{"]?([^,)}]+)`),
			regexp.MustCompile(`\.subscribe\s*\(\s*(?:Collections\.singletonList|Arrays\.asList|List\.of)\s*\(\s*"([^"]+)"`),
			regexp.MustCompile(`\.subscribe\s*\(\s*"([^"]+)"`),
		},
	}
	// Node/TS: kafkajs (producer/consumer) and node-rdkafka.
	jsPatterns = patternSet{
		produces: []*regexp.Regexp{
			regexp.MustCompile(`\.send\s*\(\s*\{[^}]*topic\s*:\s*['"]([^'"]+)['"]`),
		},
		consumes: []*regexp.Regexp{
			regexp.MustCompile(`\.subscribe\s*\(\s*\{[^}]*topic\s*:\s*['"]([^'"]+)['"]`),
			regexp.MustCompile(`topics\s*:\s*\[\s*['"]([^'"]+)['"]`),
		},
	}
	// Python: confluent-kafka, kafka-python, aiokafka.
	pyPatterns = patternSet{
		produces: []*regexp.Regexp{
			regexp.MustCompile(`\.produce\s*\(\s*["']([^"']+)["']`),
			regexp.MustCompile(`\.send\s*\(\s*["']([^"']+)["']`),
		},
		consumes: []*regexp.Regexp{
			regexp.MustCompile(`KafkaConsumer\s*\(\s*["']([^"']+)["']`),
			regexp.MustCompile(`AIOKafkaConsumer\s*\(\s*["']([^"']+)["']`),
			regexp.MustCompile(`\.subscribe\s*\(\s*\[\s*["']([^"']+)["']`),
		},
	}
)

var languageDispatch = map[string]patternSet{
	".go":    goPatterns,
	".java":  jvmPatterns,
	".kt":    jvmPatterns,
	".kts":   jvmPatterns,
	".scala": jvmPatterns,
	".js":    jsPatterns,
	".jsx":   jsPatterns,
	".ts":    jsPatterns,
	".tsx":   jsPatterns,
	".mjs":   jsPatterns,
	".py":    pyPatterns,
}

// configExts are the config-file extensions scanned for topic names. YAML only
// for now; extend if .properties/.env usage appears.
var configExts = map[string]bool{".yml": true, ".yaml": true}

func (e *Extractor) Extract(ctx context.Context, repoPath, repoName string) (*extract.Fragment, error) {
	frag := extract.NewFragment(e.Name())
	repoNodeID := "repo::" + repoName

	maxBytes := e.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = 2 * 1024 * 1024
	}

	// Aggregate topics per role to avoid duplicate edges.
	produced := map[string]occurrence{}
	consumed := map[string]occurrence{}
	referenced := map[string]occurrence{}

	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ext := strings.ToLower(filepath.Ext(path))

		info, statErr := d.Info()
		if statErr != nil || info.Size() > maxBytes {
			return nil
		}
		rel, _ := filepath.Rel(repoPath, path)
		rel = filepath.ToSlash(rel)

		if configExts[ext] {
			body, rerr := os.ReadFile(path)
			if rerr != nil {
				frag.Warn(fmt.Sprintf("%s: %v", rel, rerr))
				return nil
			}
			scanYAMLTopics(rel, string(body), produced, consumed, referenced)
			return nil
		}

		ps, ok := languageDispatch[ext]
		if !ok {
			return nil
		}

		// Scan the whole file, not line by line: struct literals split the
		// pattern across lines and the patterns span newlines.
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			frag.Warn(fmt.Sprintf("%s: %v", rel, rerr))
			return nil
		}
		content := string(body)
		for _, re := range ps.produces {
			for _, m := range re.FindAllStringSubmatchIndex(content, -1) {
				if topic := captured(content, m); topic != "" {
					register(produced, topic, rel, lineAt(content, m[0]))
				}
			}
		}
		for _, re := range ps.consumes {
			for _, m := range re.FindAllStringSubmatchIndex(content, -1) {
				if topic := captured(content, m); topic != "" {
					register(consumed, topic, rel, lineAt(content, m[0]))
				}
			}
		}
		return nil
	}

	if err := filepath.WalkDir(repoPath, walk); err != nil {
		return frag, fmt.Errorf("walk repo: %w", err)
	}

	// Drop the vague REFERENCES edge for any topic that also has a specific role.
	for topic := range referenced {
		if _, p := produced[topic]; p {
			delete(referenced, topic)
			continue
		}
		if _, c := consumed[topic]; c {
			delete(referenced, topic)
		}
	}

	// Emit the repo hub ourselves so edges don't dangle when the deps extractor
	// (which also creates it) is disabled.
	if len(produced) > 0 || len(consumed) > 0 || len(referenced) > 0 {
		frag.AddNode(extract.FragmentNode{
			ID:    repoNodeID,
			Label: repoName,
			Type:  "package",
			Metadata: map[string]any{
				"is_repository": true,
			},
		})
	}

	emitTopics(frag, repoNodeID, repoName, "produces", produced)
	emitTopics(frag, repoNodeID, repoName, "consumes", consumed)
	emitTopics(frag, repoNodeID, repoName, "references", referenced)
	return frag, nil
}

// --- YAML config topic extraction ---

// yamlKVRe matches one "key: value" line, tolerating a leading "- " marker.
var yamlKVRe = regexp.MustCompile(`^(\s*)(?:-\s+)?([A-Za-z0-9_.-]+)\s*:\s*(.*)$`)

// topicValueRe accepts plausible topic names and rejects paths, URLs, ${VAR}
// templates, and other punctuation-bearing values.
var topicValueRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,}$`)

// notTopics are placeholder/scalar values that pass topicValueRe but are
// never real topics.
var notTopics = map[string]bool{
	"tbd": true, "true": true, "false": true, "null": true, "none": true,
}

// scanYAMLTopics walks a YAML file line by line, tracking ancestor keys by
// indentation, and registers values under a topic key. Direction comes from
// producer/consumer hints in the filename, key, or an ancestor key; absent a
// hint the topic lands in referenced. Files with no "kafka" in the path or
// content are skipped so unrelated topic-named keys don't create phantom nodes.
func scanYAMLTopics(rel, contents string, produced, consumed, referenced map[string]occurrence) {
	lowerAll := strings.ToLower(contents)
	if !strings.Contains(strings.ToLower(rel), "kafka") && !strings.Contains(lowerAll, "kafka") {
		return
	}

	type frame struct {
		indent int
		key    string
	}
	var stack []frame

	for i, line := range strings.Split(contents, "\n") {
		m := yamlKVRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		indent, key, val := len(m[1]), m[2], strings.TrimSpace(m[3])

		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}

		if strings.Contains(strings.ToLower(key), "topic") && val != "" {
			ancestors := make([]string, 0, len(stack))
			for _, f := range stack {
				ancestors = append(ancestors, f.key)
			}
			target := roleMap(rel, key, ancestors, produced, consumed, referenced)
			for _, t := range topicValues(val) {
				register(target, t, rel, i+1)
			}
		}

		if val == "" {
			stack = append(stack, frame{indent: indent, key: key})
		}
	}
}

// roleMap picks the destination map from producer/consumer hints in the
// filename, key, and ancestor keys. Conflicting or absent hints fall back to
// referenced.
func roleMap(file, key string, ancestors []string, produced, consumed, referenced map[string]occurrence) map[string]occurrence {
	l := strings.ToLower(file + " " + key + " " + strings.Join(ancestors, " "))
	hasProd := strings.Contains(l, "produc")
	hasCons := strings.Contains(l, "consum")
	switch {
	case hasProd && !hasCons:
		return produced
	case hasCons && !hasProd:
		return consumed
	default:
		return referenced
	}
}

// topicValues extracts candidate topic names from a scalar or flow-style list
// value, stripping quotes and trailing # comments.
func topicValues(val string) []string {
	var raw []string
	if strings.HasPrefix(val, "[") {
		inner := strings.Trim(val, "[]")
		for _, part := range strings.Split(inner, ",") {
			raw = append(raw, strings.TrimSpace(part))
		}
	} else {
		raw = []string{val}
	}

	var out []string
	for _, v := range raw {
		if i := strings.Index(v, "#"); i >= 0 && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t') {
			v = v[:i]
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if v == "" || !topicValueRe.MatchString(v) || notTopics[strings.ToLower(v)] {
			continue
		}
		out = append(out, v)
	}
	return out
}

// captured returns the first capture group of a FindAllStringSubmatchIndex
// match, or "" when the group did not participate in the match.
func captured(s string, m []int) string {
	if len(m) >= 4 && m[2] >= 0 {
		return s[m[2]:m[3]]
	}
	return ""
}

// lineAt returns the 1-based line number of byte offset off in s.
func lineAt(s string, off int) int {
	return 1 + strings.Count(s[:off], "\n")
}

type occurrence struct {
	file string
	line int
}

func register(m map[string]occurrence, topic, file string, line int) {
	topic = strings.TrimSpace(strings.Trim(topic, `"' {}`))
	if topic == "" {
		return
	}
	if _, exists := m[topic]; !exists {
		m[topic] = occurrence{file: file, line: line}
	}
}

func emitTopics(frag *extract.Fragment, repoNodeID, repoName, relation string, topics map[string]occurrence) {
	for topic, occ := range topics {
		id := "topic::" + topic
		frag.AddNode(extract.FragmentNode{
			ID:    id,
			Label: topic,
			Type:  "kafka_topic",
			Metadata: map[string]any{
				"discovered_in_repo": repoName,
			},
		})
		frag.AddEdge(extract.FragmentEdge{
			Source:         repoNodeID,
			Target:         id,
			Relation:       relation,
			Confidence:     extract.ConfidenceInferred,
			SourceFile:     occ.file,
			SourceLocation: fmt.Sprintf("L%d", occ.line),
		})
	}
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "target", "build", "dist",
		"__pycache__", ".venv", "venv", ".tox", ".gradle", ".idea",
		".vs", "bin", "obj", ".mvn", "graphify-out":
		return true
	}
	return false
}
