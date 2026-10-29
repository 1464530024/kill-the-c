"""解析 .lnk，与系统自带快捷方式对照（只读）。"""
import struct, sys, os

FLAG_NAMES = [
    (0x00000001, "HasLinkTargetIDList"), (0x00000002, "HasLinkInfo"),
    (0x00000004, "HasName"), (0x00000008, "HasRelativePath"),
    (0x00000010, "HasWorkingDir"), (0x00000020, "HasArguments"),
    (0x00000040, "HasIconLocation"), (0x00000080, "IsUnicode"),
    (0x00000200, "ForceNoLinkInfo"), (0x00000400, "HasExpString"),
    (0x00000800, "RunInSeparateProcess"), (0x00002000, "RunAsUser"),
    (0x00004000, "HasExpIcon"), (0x00008000, "NoPidlAlias"),
    (0x00010000, "RunWithShimLayer"), (0x00020000, "ForceNoLinkTrack"),
    (0x00040000, "EnableTargetMetadata"),
]


def dump(path):
    print("=" * 78)
    print(os.path.basename(path), f"({os.path.getsize(path)} 字节)")
    b = open(path, "rb").read()
    if struct.unpack_from("<I", b, 0)[0] != 0x4C:
        print("  !! 头部非法")
        return
    flags = struct.unpack_from("<I", b, 20)[0]
    names = [n for m, n in FLAG_NAMES if flags & m]
    print(f"  LinkFlags = {flags:#010x}  ({', '.join(names)})")
    off = 76
    if flags & 0x01:
        n = struct.unpack_from("<H", b, off)[0]
        print(f"  [LinkTargetIDList] {n} 字节   ← 关键：缺失会导致资源管理器无法解析目标图标")
        off += 2 + n
    else:
        print("  [LinkTargetIDList] 缺失！")
    if flags & 0x02:
        li_size = struct.unpack_from("<I", b, off)[0]
        li_flags = struct.unpack_from("<I", b, off + 8)[0]
        lb_off = struct.unpack_from("<I", b, off + 16)[0]
        seg = b[off + lb_off: off + li_size]
        end = seg.find(b"\x00")
        print(f"  [LinkInfo] size={li_size} flags={li_flags:#x} LocalBasePath={seg[:end].decode('mbcs', 'replace')}")
        off += li_size
    uni = bool(flags & 0x80)
    def rstr(label):
        nonlocal off
        n = struct.unpack_from("<H", b, off)[0]
        off += 2
        if uni:
            s = b[off:off + n * 2].decode("utf-16-le", "replace"); off += n * 2
        else:
            s = b[off:off + n].decode("mbcs", "replace"); off += n
        if n:
            print(f"  [{label}] {s!r}")
    if flags & 0x04: rstr("NAME_STRING")
    if flags & 0x08: rstr("RELATIVE_PATH")
    if flags & 0x10: rstr("WORKING_DIR")
    if flags & 0x20: rstr("ARGUMENTS")
    if flags & 0x40: rstr("ICON_LOCATION")
    print(f"  剩余字节 = {len(b) - off}")


if __name__ == "__main__":
    for p in sys.argv[1:]:
        if os.path.exists(p):
            dump(p)
        else:
            print("不存在:", p)
