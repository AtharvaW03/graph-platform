import { useState, type FormEvent } from "react";
import { api } from "../api";
import { useAsync } from "../hooks/useAsync";
import { StatusBox } from "../components/StatusBox";
import { joinList } from "../components/DataTable";
import type { KafkaTopicInfo } from "../types";

export function KafkaPage() {
  const [topic, setTopic] = useState("");
  const { data, error, loading, run } = useAsync<KafkaTopicInfo>();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (topic.trim()) run(() => api.findKafkaTopic(topic.trim()));
  };

  return (
    <section>
      <h1>Kafka Topic</h1>
      <p className="hint">
        Exact topic name lookup — returns the repositories that produce to and
        consume from it.
      </p>
      <form onSubmit={onSubmit} className="query-form">
        <input
          value={topic}
          onChange={(e) => setTopic(e.target.value)}
          placeholder="e.g. order.created"
          autoFocus
        />
        <button type="submit">Look up</button>
      </form>
      <StatusBox loading={loading} error={error} />
      {data && (
        <dl className="kv">
          <dt>Topic</dt>
          <dd>{data.topic}</dd>
          <dt>Producers</dt>
          <dd>{joinList(data.producers)}</dd>
          <dt>Consumers</dt>
          <dd>{joinList(data.consumers)}</dd>
        </dl>
      )}
    </section>
  );
}
