"""ctypes wrapper around libgobridge.so for skills-fs.

Mirrors the Node N-API binding surface so that both languages see the
same Go-backed virtual filesystem through the same C ABI.
"""

import ctypes
import json
import os


_lib_path = os.path.join(os.path.dirname(__file__), "lib", "libgobridge.so")
_lib = ctypes.CDLL(_lib_path)

# -- function prototypes ------------------------------------------------

_lib.skills_fs_create.restype = ctypes.c_void_p
_lib.skills_fs_create.argtypes = []

_lib.skills_fs_shutdown.restype = None
_lib.skills_fs_shutdown.argtypes = [ctypes.c_void_p]

_lib.skills_fs_last_error.restype = ctypes.c_char_p
_lib.skills_fs_last_error.argtypes = [ctypes.c_void_p]

_lib.skills_fs_mount_blob.restype = ctypes.c_int
_lib.skills_fs_mount_blob.argtypes = [
    ctypes.c_void_p, ctypes.c_char_p, ctypes.c_uint,
]

_lib.skills_fs_mount_api.restype = ctypes.c_int
_lib.skills_fs_mount_api.argtypes = [
    ctypes.c_void_p, ctypes.c_char_p, ctypes.c_char_p, ctypes.c_char_p,
]

_lib.skills_fs_unmount.restype = ctypes.c_int
_lib.skills_fs_unmount.argtypes = [ctypes.c_void_p, ctypes.c_char_p]

_lib.skills_fs_rename.restype = ctypes.c_int
_lib.skills_fs_rename.argtypes = [
    ctypes.c_void_p, ctypes.c_char_p, ctypes.c_char_p,
]

_lib.skills_fs_read.restype = ctypes.c_void_p
_lib.skills_fs_read.argtypes = [
    ctypes.c_void_p, ctypes.c_char_p, ctypes.POINTER(ctypes.c_int),
]

_lib.skills_fs_write.restype = ctypes.c_int
_lib.skills_fs_write.argtypes = [
    ctypes.c_void_p, ctypes.c_char_p, ctypes.c_char_p, ctypes.c_int,
]

_lib.skills_fs_stat.restype = ctypes.c_void_p
_lib.skills_fs_stat.argtypes = [
    ctypes.c_void_p, ctypes.c_char_p, ctypes.POINTER(ctypes.c_int),
]

_lib.skills_fs_readdir.restype = ctypes.c_void_p
_lib.skills_fs_readdir.argtypes = [
    ctypes.c_void_p, ctypes.c_char_p, ctypes.POINTER(ctypes.c_int),
]

_lib.skills_fs_free.restype = None
_lib.skills_fs_free.argtypes = [ctypes.c_void_p]


class FileSystem:
    """In-process virtual filesystem backed by skills-fs core."""

    def __init__(self):
        self._handle = _lib.skills_fs_create()
        self._closed = False

    # -- public surface -------------------------------------------------

    def mount_blob(self, path, mode=0o644):
        self._assert_open()
        self._check(
            _lib.skills_fs_mount_blob(self._handle, _b(path), mode),
            f"mount_blob({path})",
        )

    def mount_api(self, path, provider_id, action):
        self._assert_open()
        self._check(
            _lib.skills_fs_mount_api(
                self._handle, _b(path), _b(provider_id), _b(action)
            ),
            f"mount_api({path})",
        )

    def unmount(self, path):
        self._assert_open()
        self._check(
            _lib.skills_fs_unmount(self._handle, _b(path)),
            f"unmount({path})",
        )

    def rename(self, old_path, new_path):
        self._assert_open()
        self._check(
            _lib.skills_fs_rename(self._handle, _b(old_path), _b(new_path)),
            f"rename({old_path} -> {new_path})",
        )

    def read(self, path):
        self._assert_open()
        out_len = ctypes.c_int()
        ptr = _lib.skills_fs_read(self._handle, _b(path), ctypes.byref(out_len))
        if not ptr:
            raise self._error(f"read({path})")
        data = ctypes.string_at(ptr, out_len.value)
        _lib.skills_fs_free(ptr)
        return data

    def write(self, path, data):
        self._assert_open()
        if isinstance(data, str):
            data = data.encode("utf-8")
        buf = ctypes.create_string_buffer(data)
        self._check(
            _lib.skills_fs_write(self._handle, _b(path), buf, len(data)),
            f"write({path})",
        )

    def stat(self, path):
        self._assert_open()
        out_len = ctypes.c_int()
        ptr = _lib.skills_fs_stat(self._handle, _b(path), ctypes.byref(out_len))
        if not ptr:
            raise self._error(f"stat({path})")
        text = ctypes.string_at(ptr, out_len.value).decode("utf-8")
        _lib.skills_fs_free(ptr)
        return json.loads(text)

    def readdir(self, path):
        self._assert_open()
        out_len = ctypes.c_int()
        ptr = _lib.skills_fs_readdir(
            self._handle, _b(path), ctypes.byref(out_len)
        )
        if not ptr:
            raise self._error(f"readdir({path})")
        text = ctypes.string_at(ptr, out_len.value).decode("utf-8")
        _lib.skills_fs_free(ptr)
        return json.loads(text)

    def shutdown(self):
        if self._closed:
            return
        _lib.skills_fs_shutdown(self._handle)
        self._closed = True

    # -- helpers --------------------------------------------------------

    def _assert_open(self):
        if self._closed:
            raise RuntimeError("FileSystem has been shut down")

    def _check(self, rc, op):
        if rc == 0:
            return
        raise self._error(op, rc)

    def _error(self, op, rc=None):
        msg = _lib.skills_fs_last_error(self._handle)
        detail = (msg.decode("utf-8") if msg else None) or (
            f"rc={rc}" if rc is not None else "failed"
        )
        return RuntimeError(f"{op}: {detail}")


def _b(s):
    """Encode str -> bytes; pass bytes through unchanged."""
    return s.encode("utf-8") if isinstance(s, str) else s
