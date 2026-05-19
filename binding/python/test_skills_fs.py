"""Smoke tests for the Python ctypes binding."""

import skills_fs


def test_blob_round_trip():
    fs = skills_fs.FileSystem()
    try:
        fs.mount_blob("/hello.txt", 0o644)
        fs.write("/hello.txt", b"hello from python")
        got = fs.read("/hello.txt")
        assert isinstance(got, bytes), "read() must return bytes"
        assert got == b"hello from python"
        print("ok  blob round-trip")
    finally:
        fs.shutdown()


def test_shutdown_idempotent():
    fs = skills_fs.FileSystem()
    fs.shutdown()
    fs.shutdown()
    print("ok  shutdown is idempotent")


def test_use_after_shutdown_raises():
    fs = skills_fs.FileSystem()
    fs.shutdown()
    try:
        fs.read("/x")
        raise AssertionError("expected RuntimeError")
    except RuntimeError as e:
        assert "shut down" in str(e).lower()
    print("ok  use-after-shutdown raises")


def test_unmount_removes_mount():
    fs = skills_fs.FileSystem()
    try:
        fs.mount_blob("/tmp.txt", 0o644)
        fs.write("/tmp.txt", b"temp")
        assert fs.read("/tmp.txt") == b"temp"

        fs.unmount("/tmp.txt")
        try:
            fs.read("/tmp.txt")
            raise AssertionError("expected RuntimeError")
        except RuntimeError as e:
            assert "ENOENT" in str(e) or "not found" in str(e).lower()
        print("ok  unmount removes mount")
    finally:
        fs.shutdown()


def test_rename_moves_data():
    fs = skills_fs.FileSystem()
    try:
        fs.mount_blob("/old.txt", 0o644)
        fs.write("/old.txt", b"payload")

        fs.rename("/old.txt", "/new.txt")
        try:
            fs.read("/old.txt")
            raise AssertionError("expected RuntimeError")
        except RuntimeError:
            pass
        assert fs.read("/new.txt") == b"payload"
        print("ok  rename moves data with path")
    finally:
        fs.shutdown()


def test_stat_reports_blob_size():
    fs = skills_fs.FileSystem()
    try:
        fs.mount_blob("/info.txt", 0o600)
        fs.write("/info.txt", b"twelve chars")

        st = fs.stat("/info.txt")
        assert st["path"] == "/info.txt"
        assert st["kind"] == "blob"
        assert st["mode"] == 0o600
        assert st["size"] == 12
        print("ok  stat reports kind, mode, size")
    finally:
        fs.shutdown()


def test_readdir_lists_builtin_sys_dir():
    fs = skills_fs.FileSystem()
    try:
        entries = fs.readdir("/sys")
        assert isinstance(entries, list), "readdir must return list"
        names = [e["name"] for e in entries]
        assert "metrics" in names, f"/sys should expose metrics, got {names}"
        print("ok  readdir lists /sys built-in entries")
    finally:
        fs.shutdown()


def test_error_messages_are_propagated():
    fs = skills_fs.FileSystem()
    try:
        try:
            fs.read("/does-not-exist.txt")
            raise AssertionError("expected RuntimeError")
        except RuntimeError as e:
            msg = str(e)
            assert msg != "read(/does-not-exist.txt): rc=-1", f"rc=-1 fallback: {msg}"
            assert len(msg) > len("read(/does-not-exist.txt): rc=-1"), f"expected real error message, got: {msg}"
        print("ok  error messages propagated")
    finally:
        fs.shutdown()


if __name__ == "__main__":
    test_blob_round_trip()
    test_shutdown_idempotent()
    test_use_after_shutdown_raises()
    test_unmount_removes_mount()
    test_rename_moves_data()
    test_stat_reports_blob_size()
    test_readdir_lists_builtin_sys_dir()
    test_error_messages_are_propagated()
    print("all tests passed")
