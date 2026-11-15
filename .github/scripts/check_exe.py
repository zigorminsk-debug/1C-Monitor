"""Проверяет собранные exe: разрядность, оконная подсистема (без окна консоли), значок и манифест."""
import struct
import sys

EXPECTED = {"x64": 0x8664, "x86": 0x14C}
failed = False
for path in sys.argv[1:]:
    data = open(path, "rb").read()
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    machine, sections = struct.unpack_from("<HH", data, pe + 4)
    optional_size = struct.unpack_from("<H", data, pe + 20)[0]
    subsystem = struct.unpack_from("<H", data, pe + 24 + 68)[0]
    names = [data[pe + 24 + optional_size + 40 * i:][:8].rstrip(b"\0").decode() for i in range(sections)]
    arch = "x64" if "x64" in path else "x86"
    problems = []
    if machine != EXPECTED[arch]:
        problems.append("разрядность 0x%x" % machine)
    if subsystem != 2:
        problems.append("подсистема %d вместо оконной (2)" % subsystem)
    if ".rsrc" not in names or b"requestedExecutionLevel" not in data:
        problems.append("нет значка или манифеста")
    print("%s: %.1f МБ, %s" % (path, len(data) / 1048576, "; ".join(problems) or "в порядке"))
    failed = failed or bool(problems)
sys.exit(1 if failed else 0)
