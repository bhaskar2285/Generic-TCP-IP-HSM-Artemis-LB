#!/bin/bash
# HSM Transparent Load Balancer — Production Deploy Script
# Deploys Spring Boot LB jar + Go EZNet binary to bare-metal
#
# Prerequisites:
#   - mvn clean package -DskipTests  (builds target/thales-transparent-lb.jar)
#   - cd go-eznet && go build -o go-eznet . (builds go-eznet binary)
#   - supervisord installed: apt install supervisor
#   - Service user created: useradd -r -s /bin/false hsm-lb
#
# Usage:
#   bash deploy/deploy.sh
#
# Directory layout on server:
#   /opt/hsm-lb/bin/          JAR + go-eznet binary
#   /opt/hsm-lb/config/lb-N/  Per-instance application.properties
#   /var/log/hsm-lb/          All logs

set -e

BIN_DIR=/opt/hsm-lb/bin
CONFIG_DIR=/opt/hsm-lb/config
LOG_DIR=/var/log/hsm-lb

LB_JAR=target/thales-transparent-lb.jar
EZNET_BIN=go-eznet/go-eznet

echo "=== HSM Transparent LB — Production Deploy ==="

# Validate build artifacts exist
[ -f "$LB_JAR" ]   || { echo "ERROR: $LB_JAR not found. Run: mvn clean package -DskipTests"; exit 1; }
[ -f "$EZNET_BIN" ] || { echo "ERROR: $EZNET_BIN not found. Run: cd go-eznet && go build -o go-eznet ."; exit 1; }

echo "Creating directories..."
sudo mkdir -p "$BIN_DIR" "$LOG_DIR"
for i in 1 2; do
  sudo mkdir -p "$CONFIG_DIR/lb-$i"
done

echo "Deploying LB jar..."
sudo cp "$LB_JAR" "$BIN_DIR/thales-transparent-lb.jar"

echo "Deploying Go EZNet binary..."
sudo cp "$EZNET_BIN" "$BIN_DIR/go-eznet"
sudo chmod +x "$BIN_DIR/go-eznet"

echo "Deploying LB configs..."
for i in 1 2; do
  sudo cp "docker/config/lb-$i/application.properties" "$CONFIG_DIR/lb-$i/application.properties"
done

echo "Deploying supervisor configs..."
sudo cp deploy/supervisor/hsm-lb.conf   /etc/supervisor/conf.d/hsm-lb.conf
sudo cp deploy/supervisor/go-eznet.conf /etc/supervisor/conf.d/go-eznet.conf

echo "Setting ownership..."
sudo chown -R hsm-lb:hsm-lb "$BIN_DIR" "$CONFIG_DIR" "$LOG_DIR"

echo "Reloading supervisord..."
sudo supervisorctl reread
sudo supervisorctl update

echo ""
echo "=== Deploy complete ==="
sudo supervisorctl status
