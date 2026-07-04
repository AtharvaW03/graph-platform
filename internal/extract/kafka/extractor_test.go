package kafka

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type topology struct {
	topics     map[string]bool
	produces   map[string]bool
	consumes   map[string]bool
	references map[string]bool
	hasHub     bool
}

func runExtract(t *testing.T, files map[string]string) topology {
	t.Helper()
	dir := t.TempDir()
	for name, contents := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	frag, err := New().Extract(context.Background(), dir, "test-repo")
	if err != nil {
		t.Fatal(err)
	}
	out := topology{
		topics:     map[string]bool{},
		produces:   map[string]bool{},
		consumes:   map[string]bool{},
		references: map[string]bool{},
	}
	for _, n := range frag.Nodes {
		if n.Type == "kafka_topic" {
			out.topics[n.Label] = true
		}
		if n.ID == "repo::test-repo" {
			out.hasHub = true
		}
	}
	for _, e := range frag.Edges {
		topic := e.Target[len("topic::"):]
		switch e.Relation {
		case "produces":
			out.produces[topic] = true
		case "consumes":
			out.consumes[topic] = true
		case "references":
			out.references[topic] = true
		}
	}
	return out
}

func TestGoTopology(t *testing.T) {
	got := runExtract(t, map[string]string{
		"kafka.go": `package main

func setup() {
	w := &kafka.Writer{Addr: addr, Topic: "orders_events", Balancer: b}
	r := kafka.NewReader(kafka.ReaderConfig{Brokers: bs, Topic: "trades_stream"})
	msg := &sarama.ProducerMessage{Topic: "audit_log", Value: v}
}
`,
	})
	if !got.produces["orders_events"] || !got.produces["audit_log"] {
		t.Errorf("produces = %v", got.produces)
	}
	if !got.consumes["trades_stream"] {
		t.Errorf("consumes = %v", got.consumes)
	}
	if !got.hasHub {
		t.Error("repo hub node missing - edges dangle when deps extractor is disabled")
	}
}

func TestGoConfluentConsumers(t *testing.T) {
	got := runExtract(t, map[string]string{
		"confluent.go": `package main

func setup() {
	c.Subscribe("order.created", nil)
	c.SubscribeTopics([]string{"margin.calculated", "risk.updated"}, rebalanceCb)
}
`,
	})
	if !got.consumes["order.created"] {
		t.Errorf("Subscribe not captured: consumes = %v", got.consumes)
	}
	if !got.consumes["margin.calculated"] {
		t.Errorf("SubscribeTopics first literal not captured: consumes = %v", got.consumes)
	}
	if len(got.produces) != 0 {
		t.Errorf("no produces expected, got %v", got.produces)
	}
}

func TestYAMLConfigTopics(t *testing.T) {
	got := runExtract(t, map[string]string{
		// filename says producer -> PRODUCES
		"resources/uat/kafka_producer.yml": `kafkaProducer:
  topicName: always-on-order-data-v2
`,
		// filename says consumer -> CONSUMES; nested consumers both captured
		"resources/uat/kafka_consumer.yml": `kafkaConsumer:
  orderConsumer:
    topicName: order-data-v2
  pqaConsumer:
    topicName: order-data-pqa-v2
`,
		// key says producer -> PRODUCES (flow list); nested topicName with no
		// hint -> REFERENCES; TBD placeholder and quoted value handled
		"resources/prod/kafka.yml": `activeDCsKafkaTopic:
  nttKafkaTopic:
    producerTopicList: ["pledge_poll", "margin_view"]
  gpxKafkaTopic:
    topicName: amx-order-data-v2 # trailing comment
  awsKafkaTopic:
    topicName: "PAYOUT"
  pendingTopic:
    topicName: TBD
`,
	})

	if !got.produces["always-on-order-data-v2"] {
		t.Errorf("producer-file topic missing: produces = %v", got.produces)
	}
	if !got.consumes["order-data-v2"] || !got.consumes["order-data-pqa-v2"] {
		t.Errorf("consumer-file topics missing: consumes = %v", got.consumes)
	}
	if !got.produces["pledge_poll"] || !got.produces["margin_view"] {
		t.Errorf("producerTopicList items missing: produces = %v", got.produces)
	}
	if !got.references["amx-order-data-v2"] || !got.references["PAYOUT"] {
		t.Errorf("unclassified topics missing from references: %v", got.references)
	}
	if got.topics["TBD"] {
		t.Error("TBD placeholder emitted as a topic")
	}
	if !got.hasHub {
		t.Error("repo hub node missing")
	}
}

func TestYAMLNonKafkaFileIgnored(t *testing.T) {
	got := runExtract(t, map[string]string{
		// "topic" key but no kafka anywhere in path or content -> skipped
		"resources/alerts.yml": `sns:
  topic: ops-alerts
`,
	})
	if len(got.topics) != 0 || got.hasHub {
		t.Errorf("non-kafka config produced nodes: %+v", got)
	}
}

func TestJVMTopology(t *testing.T) {
	got := runExtract(t, map[string]string{
		"Listener.java": `public class Listener {
    @KafkaListener(topics = "user_events")
    public void onUser(String msg) {}

    void publish() {
        template.send("notification_requests", payload);
    }
}
`,
	})
	if !got.consumes["user_events"] {
		t.Errorf("consumes = %v", got.consumes)
	}
	if !got.produces["notification_requests"] {
		t.Errorf("produces = %v", got.produces)
	}
}

func TestPythonTopology(t *testing.T) {
	got := runExtract(t, map[string]string{
		"worker.py": `consumer = KafkaConsumer("raw_ticks", bootstrap_servers=servers)
producer.send("clean_ticks", value)
`,
	})
	if !got.consumes["raw_ticks"] || !got.produces["clean_ticks"] {
		t.Errorf("topology = %+v", got)
	}
}

func TestNoKafkaNoNodes(t *testing.T) {
	got := runExtract(t, map[string]string{
		"plain.go": "package main\n\nfunc main() {}\n",
	})
	if len(got.topics) != 0 || got.hasHub {
		t.Errorf("nodes emitted for kafka-free repo: %+v", got)
	}
}
