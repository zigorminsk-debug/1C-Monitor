#!/usr/bin/env bash
# Публикует собранную версию Go + Fyne в Releases как предварительный выпуск с постоянной меткой
# go-preview: каждый запуск заменяет его свежей сборкой. Хранилище артефактов Actions при этом
# не используется. Запускается из корня репозитория; нужны GH_TOKEN и права contents: write.
set -euo pipefail

tag=go-preview
out="$RUNNER_TEMP/go-preview"
rm -rf "$out" && mkdir -p "$out"
for arch in x64 x86; do
  (cd "OneCServerMonitorGo-win-$arch" && zip -q -r "$out/1C-Server-Monitor-Go-win-$arch.zip" .)
done
(cd "$out" && sha256sum *.zip | tee SHA256SUMS.txt)

notes="$out/notes.md"
cat > "$notes" <<NOTES
Автоматическая сборка версии на Go + Fyne (каталог \`OneCMonitorGo\`). Этот выпуск заменяется при каждой сборке.

- Ветка: \`$GITHUB_REF_NAME\`
- Коммит: $GITHUB_SHA
- Сборка: $GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID
- Дата: $(date -u '+%d.%m.%Y %H:%M UTC')

Файлы: \`1C-Server-Monitor-Go-win-x64.zip\` — для 64-битной Windows, \`1C-Server-Monitor-Go-win-x86.zip\` — для 32-битной.
Требуется Windows 10 / Windows Server 2016 или новее и OpenGL 2.1; подробности — в README.txt внутри архива.
NOTES

if gh release view "$tag" >/dev/null 2>&1; then
  gh release delete "$tag" --cleanup-tag --yes
  sleep 5
fi
gh release create "$tag" --prerelease --target "$GITHUB_SHA" \
  --title "Go + Fyne: предварительная сборка" --notes-file "$notes" \
  "$out"/*.zip "$out/SHA256SUMS.txt"
url=$(gh release view "$tag" --json url -q .url)
echo "::notice title=Предварительный выпуск::$url%0A$(sed -z 's/\n/%0A/g' "$out/SHA256SUMS.txt")"
