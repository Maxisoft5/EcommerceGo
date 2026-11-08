// Manifests keeps the repetitive local Kubernetes topology reproducible.
// Run go run ./cmd/manifests, then kubectl kustomize deploy.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

type M = map[string]any

var objects []any

func add(kind, name string, spec M) {
	objects = append(objects, M{"apiVersion": api(kind), "kind": kind, "metadata": M{"name": name, "labels": M{"app.kubernetes.io/part-of": "flashcart"}}, "spec": spec})
}
func api(kind string) string {
	switch kind {
	case "Deployment", "StatefulSet":
		return "apps/v1"
	case "Job":
		return "batch/v1"
	case "HorizontalPodAutoscaler":
		return "autoscaling/v2"
	case "PodDisruptionBudget":
		return "policy/v1"
	}
	return "v1"
}
func env(k, v string) M { return M{"name": k, "value": v} }
func secret(k, key string) M {
	return M{"name": k, "valueFrom": M{"secretKeyRef": M{"name": "app-secrets", "key": key}}}
}
func keyMount(secretName string, files map[string]string) (M, []M) {
	items := []M{}
	mounts := []M{}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := files[name]
		items = append(items, M{"key": name, "path": name})
		mounts = append(mounts, M{"name": secretName, "mountPath": "/run/keys/" + path, "subPath": name, "readOnly": true})
	}
	return M{"name": secretName, "secret": M{"secretName": secretName, "defaultMode": 0444, "items": items}}, mounts
}
func resources(cpu, mem, maxcpu, maxmem string) M {
	return M{"requests": M{"cpu": cpu, "memory": mem}, "limits": M{"cpu": maxcpu, "memory": maxmem}}
}
func httpProbe(path string, port int) M {
	return M{"httpGet": M{"path": path, "port": port}, "periodSeconds": 5, "timeoutSeconds": 3, "failureThreshold": 3}
}
func service(name string, port int, headless bool) {
	spec := M{"selector": M{"app": name}, "ports": []M{{"name": "tcp", "port": port, "targetPort": port}}}
	if headless {
		spec["clusterIP"] = "None"
	}
	if name == "inventory" {
		spec["ports"] = []M{{"name": "http", "port": 8080, "targetPort": 8080}, {"name": "grpc", "port": 9090, "targetPort": 9090}}
	}
	if name == "gateway" {
		spec["type"] = "NodePort"
		spec["ports"] = []M{{"name": "https", "port": 443, "targetPort": 8080, "nodePort": 30443}}
	}
	add("Service", name, spec)
}
func app(name string) {
	labels := M{"app": name, "tier": "app"}
	vars := []M{}
	vols := []M{}
	mounts := []M{}
	keys := func(secretName string, files map[string]string) {
		v, m := keyMount(secretName, files)
		vols = append(vols, v)
		mounts = append(mounts, m...)
	}
	if name != "identity" {
		vars = append(vars, env("JWT_PUBLIC_KEY", "/run/keys/jwt-public.pem"))
		keys("jwt-verifier", map[string]string{"jwt-public.pem": "jwt-public.pem"})
	}
	if name == "gateway" || name == "catalog" || name == "projection" {
		vars = append(vars, secret("REDIS_URL", "REDIS_URL"))
	}
	switch name {
	case "identity":
		vars = append(vars, secret("DATABASE_URL", "IDENTITY_URL"), secret("ADMIN_EMAIL", "ADMIN_EMAIL"), secret("ADMIN_PASSWORD", "ADMIN_PASSWORD"), env("JWT_PRIVATE_KEY", "/run/keys/jwt-private.pem"))
		keys("jwt-signing", map[string]string{"jwt-private.pem": "jwt-private.pem"})
	case "catalog":
		vars = append(vars, secret("DATABASE_URL", "CATALOG_URL"))
	case "inventory":
		vars = append(vars, secret("DATABASE_URL", "INVENTORY_URL"))
	case "orders":
		vars = append(vars, secret("ORDERS_SHARD_0_URL", "ORDERS_SHARD_0_URL"), secret("ORDERS_SHARD_1_URL", "ORDERS_SHARD_1_URL"), env("CATALOG_URL", "http://catalog:8080"))
	case "projection":
		vars = append(vars, secret("CLICKHOUSE_PASSWORD", "CLICKHOUSE_PASSWORD"))
	case "gateway":
		vars = append(vars, secret("PUBLIC_ORIGIN", "PUBLIC_ORIGIN"), env("HTTP_TLS_CERT", "/run/keys/gateway.pem"), env("HTTP_TLS_KEY", "/run/keys/gateway-key.pem"))
		keys("gateway-tls", map[string]string{"gateway.pem": "gateway.pem", "gateway-key.pem": "gateway-key.pem"})
	}
	if name == "inventory" || name == "orders" {
		vars = append(vars, env("GRPC_CA", "/run/keys/ca.pem"), env("GRPC_CERT", "/run/keys/"+name+".pem"), env("GRPC_KEY", "/run/keys/"+name+"-key.pem"))
		keys("grpc-"+name, map[string]string{"ca.pem": "ca.pem", name + ".pem": name + ".pem", name + "-key.pem": name + "-key.pem"})
	}
	port := 8080
	if name == "gateway" {
		port = 8081
	}
	startup := httpProbe("/livez", port)
	startup["failureThreshold"] = 120
	container := M{"name": name, "image": "flashcart/" + name + ":dev", "imagePullPolicy": "IfNotPresent", "env": vars, "ports": []M{{"containerPort": 8080}, {"containerPort": 8081}, {"containerPort": 9090}}, "volumeMounts": mounts, "resources": resources("100m", "64Mi", "1000m", "256Mi"), "securityContext": M{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": M{"drop": []string{"ALL"}}}, "readinessProbe": httpProbe("/readyz", port), "livenessProbe": httpProbe("/livez", port), "startupProbe": startup}
	add("Deployment", name, M{"replicas": 1, "selector": M{"matchLabels": labels}, "strategy": M{"type": "RollingUpdate", "rollingUpdate": M{"maxUnavailable": 0, "maxSurge": 1}}, "template": M{"metadata": M{"labels": labels}, "spec": M{"automountServiceAccountToken": false, "terminationGracePeriodSeconds": 30, "securityContext": M{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "seccompProfile": M{"type": "RuntimeDefault"}}, "containers": []M{container}, "volumes": vols}}})
	service(name, 8080, false)
	if name == "gateway" || name == "orders" || name == "catalog" {
		add("HorizontalPodAutoscaler", name, M{"scaleTargetRef": M{"apiVersion": "apps/v1", "kind": "Deployment", "name": name}, "minReplicas": 1, "maxReplicas": 4, "metrics": []M{{"type": "Resource", "resource": M{"name": "cpu", "target": M{"type": "Utilization", "averageUtilization": 70}}}}, "behavior": M{"scaleDown": M{"stabilizationWindowSeconds": 120}}})
		add("PodDisruptionBudget", name, M{"maxUnavailable": 1, "selector": M{"matchLabels": labels}})
	}
}
func stateful(name, tier, image, mountPath string, port int, vars []M, command []string, probe M, cpu, mem, maxmem string) {
	labels := M{"app": name, "tier": tier}
	volumes := []M{}
	mounts := []M{{"name": "data", "mountPath": mountPath}}
	if name == "postgres" {
		volumes = append(volumes, M{"name": "init", "configMap": M{"name": "postgres-init"}})
		mounts = append(mounts, M{"name": "init", "mountPath": "/docker-entrypoint-initdb.d", "readOnly": true})
	}
	container := M{"name": name, "image": image, "env": vars, "ports": []M{{"containerPort": port}}, "volumeMounts": mounts, "resources": resources(cpu, mem, "2000m", maxmem), "readinessProbe": probe, "startupProbe": M{"tcpSocket": M{"port": port}, "periodSeconds": 5, "failureThreshold": 120}}
	if len(command) > 0 {
		container["command"] = command
	}
	pod := M{"automountServiceAccountToken": false, "terminationGracePeriodSeconds": 30, "containers": []M{container}, "volumes": volumes}
	if name == "kafka" || name == "opensearch" {
		pod["securityContext"] = M{"fsGroup": 1000}
	}
	add("StatefulSet", name, M{"serviceName": name, "replicas": 1, "selector": M{"matchLabels": labels}, "template": M{"metadata": M{"labels": labels}, "spec": pod}, "volumeClaimTemplates": []M{{"metadata": M{"name": "data"}, "spec": M{"accessModes": []string{"ReadWriteOnce"}, "resources": M{"requests": M{"storage": "2Gi"}}}}}})
	service(name, port, true)
}
func main() {
	for _, name := range []string{"gateway", "identity", "catalog", "inventory", "orders", "projection"} {
		app(name)
	}
	postgresVars := []M{env("POSTGRES_USER", "postgres"), env("POSTGRES_DB", "postgres"), env("PGDATA", "/var/lib/postgresql/data/pgdata"), secret("POSTGRES_PASSWORD", "POSTGRES_PASSWORD"), secret("IDENTITY_DB_PASSWORD", "IDENTITY_DB_PASSWORD"), secret("CATALOG_DB_PASSWORD", "CATALOG_DB_PASSWORD"), secret("INVENTORY_DB_PASSWORD", "INVENTORY_DB_PASSWORD")}
	pgprobe := M{"exec": M{"command": []string{"sh", "-ec", "pg_isready -U \"$POSTGRES_USER\" -d \"$POSTGRES_DB\""}}, "periodSeconds": 5, "timeoutSeconds": 3}
	stateful("postgres", "database", "postgres:17.6-alpine", "/var/lib/postgresql/data", 5432, postgresVars, nil, pgprobe, "100m", "128Mi", "512Mi")
	for _, name := range []string{"orders-db-0", "orders-db-1"} {
		stateful(name, "order-db", "postgres:17.6-alpine", "/var/lib/postgresql/data", 5432, []M{env("POSTGRES_USER", "orders"), env("POSTGRES_DB", "orders"), env("PGDATA", "/var/lib/postgresql/data/pgdata"), secret("POSTGRES_PASSWORD", "ORDERS_DB_PASSWORD")}, nil, pgprobe, "100m", "128Mi", "512Mi")
	}
	kafkaVars := []M{env("KAFKA_NODE_ID", "1"), env("KAFKA_PROCESS_ROLES", "broker,controller"), env("KAFKA_LISTENERS", "PLAINTEXT://:9092,CONTROLLER://:9093"), env("KAFKA_ADVERTISED_LISTENERS", "PLAINTEXT://kafka:9092"), env("KAFKA_LISTENER_SECURITY_PROTOCOL_MAP", "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT"), env("KAFKA_CONTROLLER_LISTENER_NAMES", "CONTROLLER"), env("KAFKA_CONTROLLER_QUORUM_VOTERS", "1@kafka:9093"), env("KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR", "1"), env("KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR", "1"), env("KAFKA_TRANSACTION_STATE_LOG_MIN_ISR", "1"), env("KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS", "0"), env("KAFKA_AUTO_CREATE_TOPICS_ENABLE", "false"), env("KAFKA_LOG_DIRS", "/var/lib/kafka/data"), env("CLUSTER_ID", "MkU3OEVBNTcwNTJENDM2Qk")}
	stateful("kafka", "broker", "apache/kafka:4.1.2", "/var/lib/kafka/data", 9092, kafkaVars, nil, M{"tcpSocket": M{"port": 9092}, "periodSeconds": 5}, "250m", "768Mi", "1536Mi")
	stateful("redis", "cache", "redis:7.4.6-alpine", "/data", 6379, []M{secret("REDIS_PASSWORD", "REDIS_PASSWORD")}, []string{"sh", "-ec", "exec redis-server --appendonly yes --requirepass \"$REDIS_PASSWORD\" --maxmemory 256mb --maxmemory-policy noeviction"}, M{"exec": M{"command": []string{"sh", "-ec", "REDISCLI_AUTH=\"$REDIS_PASSWORD\" redis-cli ping | grep PONG"}}, "periodSeconds": 5}, "100m", "64Mi", "384Mi")
	stateful("opensearch", "projection-db", "opensearchproject/opensearch:3.3.2", "/usr/share/opensearch/data", 9200, []M{env("discovery.type", "single-node"), env("DISABLE_SECURITY_PLUGIN", "true"), env("DISABLE_INSTALL_DEMO_CONFIG", "true"), env("OPENSEARCH_JAVA_OPTS", "-Xms512m -Xmx512m")}, nil, httpProbe("/_cluster/health", 9200), "250m", "768Mi", "1536Mi")
	stateful("clickhouse", "projection-db", "clickhouse/clickhouse-server:25.8.3.66-alpine", "/var/lib/clickhouse", 8123, []M{env("CLICKHOUSE_DB", "commerce"), env("CLICKHOUSE_USER", "flashcart"), secret("CLICKHOUSE_PASSWORD", "CLICKHOUSE_PASSWORD"), env("CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT", "1")}, nil, httpProbe("/ping", 8123), "250m", "256Mi", "1Gi")
	command := `until /opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:9092 --list; do sleep 3; done
/opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:9092 --create --if-not-exists --topic commerce.events.v1 --partitions 12 --replication-factor 1
/opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:9092 --create --if-not-exists --topic commerce.dlq.v1 --partitions 3 --replication-factor 1`
	add("Job", "kafka-init", M{"backoffLimit": 5, "template": M{"metadata": M{"labels": M{"app": "kafka-init"}}, "spec": M{"automountServiceAccountToken": false, "restartPolicy": "OnFailure", "containers": []M{{"name": "init", "image": "apache/kafka:4.1.2", "command": []string{"sh", "-ec", command}, "resources": resources("100m", "128Mi", "1000m", "512Mi")}}}}})
	b, e := json.MarshalIndent(M{"apiVersion": "v1", "kind": "List", "items": objects}, "", "  ")
	if e != nil {
		panic(e)
	}
	if e = os.MkdirAll("deploy/k8s", 0755); e != nil {
		panic(e)
	}
	if e = os.WriteFile("deploy/k8s/generated.json", append(b, '\n'), 0644); e != nil {
		panic(e)
	}
	fmt.Printf("Generated %d Kubernetes objects\n", len(objects))
}
