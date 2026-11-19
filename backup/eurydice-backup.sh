#!/usr/bin/env bash
# Backup do Eurydice que o próprio servidor não consegue ler.
#
#   dump do banco + chaves GPG/JWT do servidor + configuração
#   | tar | zstd -19 | age -R recipients.txt   (só chaves PÚBLICAS no servidor)
#   -> pasta local com retenção + cópia opcional para um storage com bloqueio de objeto
#
# Variáveis (ou valores padrão):
#   EURYDICE_DIR      pasta com o compose (padrão: ../deploy relativo a este script)
#   BACKUP_DIR        onde guardar os .age (padrão: /var/backups/eurydice)
#   RECIPIENTS        arquivo com as chaves públicas age dos custodiantes
#   KEEP_DAYS         retenção local (padrão: 14)
#   RCLONE_REMOTE     ex.: "cofre-backup:eurydice" (vazio = sem cópia remota)
set -euo pipefail
umask 077

here="$(cd "$(dirname "$0")" && pwd)"
EURYDICE_DIR="${EURYDICE_DIR:-$here/../deploy}"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/eurydice}"
RECIPIENTS="${RECIPIENTS:-$here/recipients.txt}"
KEEP_DAYS="${KEEP_DAYS:-14}"
RCLONE_REMOTE="${RCLONE_REMOTE:-}"

log() { printf '{"event":"%s","ts":"%s"%s}\n' "$1" "$(date -u +%FT%TZ)" "${2:+,$2}"; }
die() { log error "\"error\":\"$1\""; exit 1; }

command -v age >/dev/null || die "age não instalado"
command -v zstd >/dev/null || die "zstd não instalado"
[[ -s "$RECIPIENTS" ]] || die "sem destinatários em $RECIPIENTS"
# a regra de ouro: nenhuma chave secreta age neste servidor
if grep -q "AGE-SECRET-KEY" "$RECIPIENTS"; then die "recipients.txt contém chave SECRETA: use só as públicas (age1...)"; fi

dc() { docker compose --project-directory "$EURYDICE_DIR" "$@"; }

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/eurydice" "$BACKUP_DIR"

# 1. dump consistente sem travar o banco (as senhas já estão cifradas por PGP)
dc exec -T db sh -c 'mariadb-dump --single-transaction --routines --triggers -u"$MARIADB_USER" -p"$MARIADB_PASSWORD" "$MARIADB_DATABASE"' \
  > "$work/eurydice/passbolt.sql" || die "dump falhou"

# 2. chaves do servidor: sem elas o dump não volta a funcionar
dc exec -T passbolt tar -C /etc/passbolt -cf - gpg jwt > "$work/eurydice/server-keys.tar" || die "cópia das chaves falhou"

# 3. configuração; o .env tem segredos, por isso só sai daqui dentro do pacote cifrado
cp "$EURYDICE_DIR/compose.yaml" "$work/eurydice/"
[[ -f "$EURYDICE_DIR/.env" ]] && cp "$EURYDICE_DIR/.env" "$work/eurydice/env"

# 4. manifesto para o teste de restauração conferir
users="$(dc exec -T db sh -c 'mariadb -N -u"$MARIADB_USER" -p"$MARIADB_PASSWORD" "$MARIADB_DATABASE" -e "SELECT COUNT(*) FROM users WHERE deleted=0"')"
resources="$(dc exec -T db sh -c 'mariadb -N -u"$MARIADB_USER" -p"$MARIADB_PASSWORD" "$MARIADB_DATABASE" -e "SELECT COUNT(*) FROM resources WHERE deleted=0"')"
{
  echo "created=$stamp"
  echo "users=$users"
  echo "resources=$resources"
  echo "passbolt_image=$(dc images passbolt --format '{{.Repository}}:{{.Tag}}' 2>/dev/null | head -1)"
  (cd "$work/eurydice" && sha256sum -- * | sed 's/^/sha256 /')
} > "$work/eurydice/MANIFEST"

# 5. empacota, comprime e cifra para os custodiantes
out="$BACKUP_DIR/eurydice-$stamp.tar.zst.age"
tar -C "$work" -cf - eurydice | zstd -19 -q | age -R "$RECIPIENTS" > "$out"
(cd "$BACKUP_DIR" && sha256sum "$(basename "$out")" > "$(basename "$out").sha256")
log backup_ok "\"file\":\"$(basename "$out")\",\"bytes\":$(wc -c < "$out" | tr -d ' '),\"users\":$users,\"resources\":$resources"

# 6. cópia remota. O token deve poder GRAVAR, não apagar; o bucket deve ter bloqueio de objeto.
if [[ -n "$RCLONE_REMOTE" ]]; then
  # --s3-no-head: alguns storages "compatíveis com S3" respondem 501 ao HEAD ?versionId= pós-upload
  for f in "$out" "$out.sha256"; do
    rclone copyto --s3-no-head "$f" "$RCLONE_REMOTE/$(basename "$f")" || die "upload falhou: $(basename "$f")"
  done
  log upload_ok "\"remote\":\"$RCLONE_REMOTE\""
fi

# 7. retenção local
find "$BACKUP_DIR" -name 'eurydice-*.tar.zst.age*' -mtime "+$KEEP_DAYS" -delete
date -u +%FT%TZ > "$BACKUP_DIR/.last-success"
