#!/usr/bin/env bash
# Teste de restauração: só conta como backup o que passou aqui.
# Roda na máquina de um CUSTODIANTE (que tem a chave secreta age), nunca no servidor.
#
#   ./restore-test.sh eurydice-20260101T023000Z.tar.zst.age ~/.config/eurydice/custodiante.agekey
#
# Passos: confere o sha256 -> decifra -> sobe um Passbolt isolado (sem porta publicada)
# -> restaura banco e chaves -> healthcheck -> compara usuários/recursos com o MANIFEST -> derruba tudo.
set -euo pipefail

file="${1:?uso: $0 <backup.tar.zst.age> <identidade.agekey>}"
identity="${2:?informe a identidade age do custodiante}"
project="eurydice-restoretest-$$"
work="$(mktemp -d)"
cleanup() {
  docker compose -p "$project" -f "$work/restore.yaml" down -v >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

if [[ -f "$file.sha256" ]]; then
  (cd "$(dirname "$file")" && sha256sum -c "$(basename "$file").sha256") || { echo "sha256 não confere"; exit 1; }
fi

age -d -i "$identity" "$file" | zstd -d | tar -C "$work" -xf -
manifest="$work/eurydice/MANIFEST"
want_users="$(sed -n 's/^users=//p' "$manifest")"
want_res="$(sed -n 's/^resources=//p' "$manifest")"
image="$(sed -n 's/^passbolt_image=//p' "$manifest")"
image="${image:-passbolt/passbolt:latest-ce}"
mkdir -p "$work/keys" && tar -C "$work/keys" -xf "$work/eurydice/server-keys.tar"

pass="$(openssl rand -hex 16)"
cat > "$work/restore.yaml" <<YAML
services:
  db:
    image: mariadb:11.4
    environment: { MARIADB_RANDOM_ROOT_PASSWORD: "true", MARIADB_DATABASE: passbolt, MARIADB_USER: passbolt, MARIADB_PASSWORD: "$pass" }
    healthcheck: { test: ["CMD", "healthcheck.sh", "--connect", "--innodb_initialized"], interval: 5s, retries: 30 }
    networks: [iso]
  passbolt:
    image: $image
    depends_on: { db: { condition: service_healthy } }
    environment:
      APP_FULL_BASE_URL: https://restore.invalid
      DATASOURCES_DEFAULT_HOST: db
      DATASOURCES_DEFAULT_DATABASE: passbolt
      DATASOURCES_DEFAULT_USERNAME: passbolt
      DATASOURCES_DEFAULT_PASSWORD: "$pass"
      EMAIL_TRANSPORT_DEFAULT_HOST: localhost
    volumes:
      - $work/keys/gpg:/etc/passbolt/gpg
      - $work/keys/jwt:/etc/passbolt/jwt
    networks: [iso]
networks:
  iso: { internal: true }
YAML

dc() { docker compose -p "$project" -f "$work/restore.yaml" "$@"; }
dc up -d db
dc exec -T db sh -c 'until mariadb -u passbolt -p"$MARIADB_PASSWORD" -e "select 1" passbolt >/dev/null 2>&1; do sleep 2; done'
dc exec -T db sh -c 'mariadb -u passbolt -p"$MARIADB_PASSWORD" passbolt' < "$work/eurydice/passbolt.sql"
dc up -d passbolt
sleep 20
dc exec -T passbolt su -s /bin/bash -c "/usr/share/php/passbolt/bin/cake passbolt healthcheck" www-data | tail -5

got_users="$(dc exec -T db sh -c 'mariadb -N -u passbolt -p"$MARIADB_PASSWORD" passbolt -e "SELECT COUNT(*) FROM users WHERE deleted=0"')"
got_res="$(dc exec -T db sh -c 'mariadb -N -u passbolt -p"$MARIADB_PASSWORD" passbolt -e "SELECT COUNT(*) FROM resources WHERE deleted=0"')"
echo "usuários: $got_users/$want_users · recursos: $got_res/$want_res"
[[ "$got_users" == "$want_users" && "$got_res" == "$want_res" ]] && echo "RESTAURAÇÃO OK" || { echo "RESTAURAÇÃO DIVERGE"; exit 1; }
