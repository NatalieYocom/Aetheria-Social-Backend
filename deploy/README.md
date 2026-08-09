# Deploy

Production Compose для одного сервера. API, workers и Caddy запускаются здесь;
PostgreSQL и Redis должны быть внешними.

## Запуск

```bash
cd deploy
cp .env.production.example .env
mkdir -p secrets
```

Заполнить `.env` и создайть три файла:

```text
secrets/database_url
secrets/redis_url
secrets/jwt_secret
```

После этого:

```bash
chmod 600 .env secrets/*
docker compose --env-file .env -f docker-compose.production.yml config --quiet
docker compose --env-file .env -f docker-compose.production.yml pull
docker compose --env-file .env -f docker-compose.production.yml up -d
docker compose --env-file .env -f docker-compose.production.yml ps -a
```

DNS домена должен указывать на сервер, порты `80` и `443` должны быть открыты.
Caddy получит TLS-сертификат автоматически. Миграции выполняются перед запуском
API; перед обновлением образа обязательно сделайте резервную копию PostgreSQL.

Для запуска OpenTelemetry Collector добавить `--profile telemetry` к команде
`up -d`.
