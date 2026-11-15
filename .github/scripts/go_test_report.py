"""Печатает результат `go test -json` обычным текстом и дублирует вывод тестов в аннотации запуска:
упавшие тесты — как ошибки, тесты Windows API — как заметки. Аннотации видны на странице запуска
и доступны через API, в отличие от журнала шага."""
import json
import sys

CHUNK = 1800  # знаков в одной аннотации
LIMIT = 9     # аннотаций одного уровня на шаг (GitHub показывает не больше десяти)

out = sys.stdout.buffer
tests, order, failed_build = {}, [], []
for raw in open(sys.argv[1], encoding="utf-8", errors="replace"):
    try:
        event = json.loads(raw)
    except ValueError:
        failed_build.append(raw.rstrip())
        continue
    name = event.get("Test")
    if event.get("Action") in ("output", "build-output"):
        text = event.get("Output", "")
        out.write(text.encode("utf-8"))
        if name is None:
            if not text.startswith(("ok ", "PASS", "FAIL", "exit status", "?")):
                failed_build.append(text.rstrip())
            continue
        if name not in tests:
            tests[name] = {"lines": [], "result": ""}
            order.append(name)
        if not text.startswith(("=== ", "--- ")):
            tests[name]["lines"].append(text.rstrip())
    elif name in tests and event.get("Action") in ("pass", "fail", "skip"):
        tests[name]["result"] = event["Action"]


def annotate(level, title, lines, budget):
    chunk, part = "", 1
    for line in lines + [None]:
        if line is None or len(chunk) + len(line) > CHUNK:
            if chunk and budget[level] > 0:
                budget[level] -= 1
                message = chunk.replace("%", "%25").replace("\r", "").replace("\n", "%0A")
                out.write(("::%s title=%s (%d)::%s\n" % (level, title, part, message)).encode("utf-8"))
                part += 1
            chunk = ""
        if line is not None:
            chunk += line.strip()[:CHUNK - 1] + "\n"


budget = {"error": LIMIT, "notice": LIMIT}
if failed_build:
    annotate("error", "go test", failed_build, budget)
for name in order:
    test = tests[name]
    if test["result"] == "fail" or not test["result"]:
        annotate("error", name, test["lines"], budget)
for name in order:
    if tests[name]["result"] == "pass" and name.startswith(("TestWindows", "TestWindow")):
        annotate("notice", name, tests[name]["lines"], budget)
out.flush()
