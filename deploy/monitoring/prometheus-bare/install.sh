#!/bin/bash
# Install Prometheus + Grafana on bare-metal (Ubuntu/Debian)
# Run as root or with sudo
set -e

PROMETHEUS_VERSION=2.52.0
GRAFANA_VERSION=11.0.0
INSTALL_USER=prometheus

echo "=== Installing Prometheus ==="
useradd -r -s /bin/false $INSTALL_USER 2>/dev/null || true

cd /tmp
wget -q "https://github.com/prometheus/prometheus/releases/download/v${PROMETHEUS_VERSION}/prometheus-${PROMETHEUS_VERSION}.linux-amd64.tar.gz"
tar xzf "prometheus-${PROMETHEUS_VERSION}.linux-amd64.tar.gz"
cp "prometheus-${PROMETHEUS_VERSION}.linux-amd64/prometheus" /usr/local/bin/
cp "prometheus-${PROMETHEUS_VERSION}.linux-amd64/promtool"   /usr/local/bin/

mkdir -p /etc/prometheus /var/lib/prometheus
cp "prometheus-${PROMETHEUS_VERSION}.linux-amd64/consoles"       /etc/prometheus/ -r
cp "prometheus-${PROMETHEUS_VERSION}.linux-amd64/console_libraries" /etc/prometheus/ -r
chown -R $INSTALL_USER:$INSTALL_USER /etc/prometheus /var/lib/prometheus

# Copy our scrape config
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cp "$SCRIPT_DIR/prometheus.yml" /etc/prometheus/prometheus.yml
echo "Edit /etc/prometheus/prometheus.yml — replace placeholder hostnames"

# Systemd unit
cat > /etc/systemd/system/prometheus.service <<'EOF'
[Unit]
Description=Prometheus
After=network.target

[Service]
User=prometheus
ExecStart=/usr/local/bin/prometheus \
  --config.file=/etc/prometheus/prometheus.yml \
  --storage.tsdb.path=/var/lib/prometheus \
  --storage.tsdb.retention.time=30d \
  --web.listen-address=0.0.0.0:9090
Restart=always

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable prometheus
systemctl start prometheus
echo "Prometheus running on :9090"

echo ""
echo "=== Installing Grafana ==="
apt-get install -y apt-transport-https software-properties-common wget gnupg
wget -q -O - https://packages.grafana.com/gpg.key | apt-key add -
echo "deb https://packages.grafana.com/oss/deb stable main" > /etc/apt/sources.list.d/grafana.list
apt-get update -q
apt-get install -y "grafana=${GRAFANA_VERSION}"

# Copy provisioning
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
cp -r "$REPO_ROOT/docker/grafana/provisioning" /etc/grafana/provisioning/
chown -R grafana:grafana /etc/grafana/provisioning

# Point datasource at localhost
sed -i 's|http://prometheus:9090|http://localhost:9090|g' \
  /etc/grafana/provisioning/datasources/prometheus.yml

systemctl daemon-reload
systemctl enable grafana-server
systemctl start grafana-server
echo "Grafana running on :3000 (admin/admin — change on first login)"

echo ""
echo "=== Install complete ==="
echo "  Prometheus : http://$(hostname -I | awk '{print $1}'):9090"
echo "  Grafana    : http://$(hostname -I | awk '{print $1}'):3000"
