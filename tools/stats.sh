#!/bin/sh
# Сколько раз шлюз ставили и обновляли, по данным GitHub.
#
# Скачивания install.sh — это запуски установщика, а бинарника — установщик
# плюс обновления кнопкой в вебе. Разница между ними и есть обновления из веба.
#
# Считает только то, что берут из релизов: проверки версии и обращения к raw
# GitHub не учитывает вовсе.
set -eu

REPO="${REPO:-staskuznec/mqtt2mimismart}"

curl -fsSL "https://api.github.com/repos/$REPO/releases" | python3 -c '
import json, sys

rels = json.load(sys.stdin)
if isinstance(rels, dict):
    print("GitHub ответил:", rels.get("message", "?"))
    sys.exit(1)

installs = binaries = 0
rows = []
for r in sorted(rels, key=lambda r: r["tag_name"]):
    ins = sum(a["download_count"] for a in r["assets"] if a["name"] == "install.sh")
    bins = sum(a["download_count"] for a in r["assets"]
               if a["name"].startswith("mqtt2mimismart-linux"))
    installs += ins
    binaries += bins
    if ins or bins:
        rows.append((r["tag_name"], ins, bins))

print("версия      установщик  бинарники")
for tag, ins, bins in rows:
    print(f"{tag:12s} {ins:10d} {bins:10d}")

print()
print(f"  запусков установщика: {installs}")
print(f"  скачано бинарников:   {binaries}")
print(f"  обновлений из веба:   {max(binaries - installs, 0)} (оценка)")
'
