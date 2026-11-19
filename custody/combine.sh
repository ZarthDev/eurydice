#!/usr/bin/env bash
# Junta 2 das 3 partes e abre o material da conta de custódia.
# Usado na recuperação: dois custodiantes juntos, nunca um sozinho.
#
#   ./combine.sh out/custodia-material.tar.age <pasta-de-saida>
#   (o ssss-combine pede as 2 partes, uma por linha)
set -euo pipefail
umask 077

material="${1:?uso: $0 <custodia-material.tar.age> <pasta-de-saida>}"
dest="${2:?informe a pasta de saída}"
for bin in age ssss-combine; do command -v "$bin" >/dev/null || { echo "instale: $bin"; exit 1; }; done

mkdir -p "$dest"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
echo "Cole 2 partes (uma por linha):"
ssss-combine -t 2 -q 2> "$tmp/secret" < /dev/tty || true
secret="$(grep -o 'AGE-SECRET-KEY-[A-Z0-9]*' "$tmp/secret" || true)"
[[ -n "$secret" ]] || { echo "não foi possível reconstruir a chave (partes erradas?)"; exit 1; }
printf '%s\n' "$secret" > "$tmp/id.agekey"
age -d -i "$tmp/id.agekey" "$material" | tar -C "$dest" -xf -
echo "Material aberto em $dest. Use, recompartilhe os grupos e APAGUE a pasta ao terminar."
