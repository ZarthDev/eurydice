#!/usr/bin/env bash
# Cerimônia de custódia: protege o material da conta de custódia com Shamir 2-de-3.
#
# Nada de criptografia caseira: usamos duas ferramentas consagradas.
#   1. age-keygen gera uma identidade nova (a "chave da custódia");
#   2. o material sensível (chave privada PGP + frase-senha da conta de custódia
#      no Passbolt) é cifrado para essa identidade com age;
#   3. a identidade é dividida com ssss-split em 3 partes, das quais 2 bastam.
#
# Rode numa máquina confiável, offline se possível. Ao final, entregue uma parte
# para cada custodiante (papel, cofre físico, gerenciador pessoal) e apague o resto.
#
#   ./split.sh custodia-privada.asc custodia-frase.txt
set -euo pipefail
umask 077

key="${1:?uso: $0 <chave-privada-da-custodia.asc> <frase-senha.txt>}"
phrase="${2:?informe o arquivo com a frase-senha da conta de custódia}"
for bin in age age-keygen ssss-split; do command -v "$bin" >/dev/null || { echo "instale: $bin"; exit 1; }; done

out="$(cd "$(dirname "$0")" && pwd)/out"
mkdir -p "$out"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

age-keygen -o "$tmp/custodia.agekey" 2>/dev/null
recipient="$(age-keygen -y "$tmp/custodia.agekey")"
secret="$(grep '^AGE-SECRET-KEY' "$tmp/custodia.agekey")"

tar -C "$(dirname "$key")" -cf - "$(basename "$key")" -C "$(cd "$(dirname "$phrase")" && pwd)" "$(basename "$phrase")" \
  | age -r "$recipient" > "$out/custodia-material.tar.age"

# 2-de-3; o rótulo "eurydice" ajuda a não misturar com outras partes
printf '%s\n' "$secret" | ssss-split -t 2 -n 3 -w eurydice -q > "$out/partes.txt"

echo
echo "Pronto:"
echo "  $out/custodia-material.tar.age  -> pode ficar junto do backup (sozinho não abre nada)"
echo "  $out/partes.txt                 -> 3 linhas: UMA para cada custodiante, depois apague o arquivo"
echo
echo "Confira antes de apagar qualquer coisa: ./combine.sh com duas partes tem que abrir o material."
