#!/bin/bash
# Install JMX Prometheus agent on bare-metal Artemis brokers
# Run on each Artemis host
set -e

JMX_AGENT_VERSION=1.0.1
ARTEMIS_ETC=/opt/artemis/etc   # adjust to your Artemis instance etc/ path
AGENT_JAR=/opt/artemis/lib/jmx_prometheus_javaagent.jar

echo "Downloading JMX Prometheus agent..."
wget -q -O "$AGENT_JAR" \
  "https://repo1.maven.org/maven2/io/prometheus/jmx/jmx_prometheus_javaagent/${JMX_AGENT_VERSION}/jmx_prometheus_javaagent-${JMX_AGENT_VERSION}.jar"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cp "$SCRIPT_DIR/../../../docker/prometheus/jmx-artemis.yml" /opt/artemis/etc/jmx-artemis.yml

echo ""
echo "Add to Artemis JVM args in artemis.profile or broker startup:"
echo "  -javaagent:/opt/artemis/lib/jmx_prometheus_javaagent.jar=9404:/opt/artemis/etc/jmx-artemis.yml"
echo ""
echo "Then restart Artemis. Metrics available at http://BROKER_HOST:9404/metrics"
