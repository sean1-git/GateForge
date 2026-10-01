#!/bin/sh
set -eu
mkdir -p .cache
password=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
printf 'export GATEFORGE_TEST_DATABASE_URL=postgres://postgres:%s@gateforge-test-postgres:5432/gateforge_test?sslmode=disable\nexport GATEFORGE_TEST_REDIS_URL=redis://gateforge-test-redis:6379/0\nexport GATEFORGE_TEST_BACKUP_RESTORE=1\n' "$password" > .cache/cloud-tests.env
chmod 600 .cache/cloud-tests.env
docker run -d --name gateforge-test-postgres --network cloudbuild -e POSTGRES_PASSWORD="$password" -e POSTGRES_DB=gateforge_test postgres:17-alpine
docker run -d --name gateforge-test-redis --network cloudbuild redis:7.2-alpine
for attempt in $(seq 1 30); do
  if docker exec gateforge-test-postgres pg_isready -U postgres -d gateforge_test >/dev/null && docker exec gateforge-test-redis redis-cli ping >/dev/null; then exit 0; fi
  sleep 1
done
echo 'Disposable test services did not become ready' >&2
exit 1
