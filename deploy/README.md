# Деплой в Kubernetes

Helm-чарт [`helm/marketpulse`](helm/marketpulse) разворачивает весь стек: Postgres, Kafka (KRaft) и
ClickHouse как StatefulSet с PVC, а Go-сервисы, MCP-сервер, агента и дашборд как Deployment + Service.
Проверен на kind (локально и в CI: `helm-kind` в [`ci.yml`](../.github/workflows/ci.yml)).
Это учебный деплой на одну ноду: по одной реплике, демо-пароли в `values.yaml`.

```bash
# образы (для kind — загрузить в кластер)
for s in collector mock-marketplace; do docker build -f go.Dockerfile --build-arg SERVICE=$s -t marketpulse/$s:local .; done
for s in mcp-server agent-api dashboard; do docker build -t marketpulse/$s:local $s; done
kind create cluster --name mp
for s in collector mock-marketplace mcp-server agent-api dashboard; do kind load docker-image marketpulse/$s:local --name mp; done

helm install mp deploy/helm/marketpulse --set secrets.llmApiKey=<ключ OpenRouter>
kubectl port-forward svc/dashboard 3000:3000
```

## Нюансы, на которые пришлось наступить
- `enableServiceLinks: false` во всех подах: иначе Kubernetes добавляет переменные вроде `KAFKA_PORT=tcp://...`,
  и образ Kafka читает их как свою конфигурацию.
- Кворум KRaft указан как `1@localhost:9093`, а не `kafka:9093`. Пока под не Ready, у Service нет endpoints,
  и контроллер не смог бы достучаться до самого себя.
- Init-скрипты ClickHouse выполняются только при пустом каталоге данных, поэтому схема лежит в ConfigMap
  и монтируется в `/docker-entrypoint-initdb.d`. Копии файлов из `clickhouse/` в `files/` чарта проверяются `diff` в CI.
- Пароль Postgres в `PG_DSN` коллектора подставляется через `$(PG_PASSWORD)` из Secret, поэтому `secretEnv`
  в шаблоне идёт раньше обычных `env`.

## Мониторинг
`docker compose --profile monitoring up` поднимает Prometheus и Grafana (порт 13000): источник данных и дашборд
[`marketpulse.json`](../monitoring/grafana/dashboards/marketpulse.json) подключаются автоматически. Панели: очередь outbox,
скорость публикации в Kafka, дедупликация, запросы к API по кодам ответа (видно 429 и работу backoff), p95 цикла синхронизации.
В Kubernetes у подов коллектора и эмулятора стоят аннотации `prometheus.io/scrape`.
