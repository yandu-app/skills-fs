#include <node_api.h>
#include <stdint.h>
#include <string.h>
#include "libgobridge.h"

#define NAPI_CALL(env, call)                                                   \
  do {                                                                         \
    napi_status status = (call);                                               \
    if (status != napi_ok) {                                                   \
      napi_throw_error((env), NULL, #call " failed");                          \
      return NULL;                                                             \
    }                                                                          \
  } while (0)

static napi_value CreateFS(napi_env env, napi_callback_info info) {
  napi_value result;
  uintptr_t handle = skills_fs_create();
  NAPI_CALL(env, napi_create_bigint_uint64(env, handle, &result));
  return result;
}

static napi_value Shutdown(napi_env env, napi_callback_info info) {
  size_t argc = 1;
  napi_value args[1];
  NAPI_CALL(env, napi_get_cb_info(env, info, &argc, args, NULL, NULL));

  bool lossless;
  uint64_t handle;
  NAPI_CALL(env, napi_get_value_bigint_uint64(env, args[0], &handle, &lossless));

  skills_fs_shutdown((uintptr_t)handle);
  return NULL;
}

static napi_value MountBlob(napi_env env, napi_callback_info info) {
  size_t argc = 3;
  napi_value args[3];
  NAPI_CALL(env, napi_get_cb_info(env, info, &argc, args, NULL, NULL));

  bool lossless;
  uint64_t handle;
  NAPI_CALL(env, napi_get_value_bigint_uint64(env, args[0], &handle, &lossless));

  size_t path_len;
  char path_buf[4096];
  NAPI_CALL(env, napi_get_value_string_utf8(env, args[1], path_buf, sizeof(path_buf), &path_len));

  uint32_t mode;
  NAPI_CALL(env, napi_get_value_uint32(env, args[2], &mode));

  int rc = skills_fs_mount_blob((uintptr_t)handle, path_buf, mode);
  napi_value result;
  NAPI_CALL(env, napi_create_int32(env, rc, &result));
  return result;
}

static napi_value Read(napi_env env, napi_callback_info info) {
  size_t argc = 2;
  napi_value args[2];
  NAPI_CALL(env, napi_get_cb_info(env, info, &argc, args, NULL, NULL));

  bool lossless;
  uint64_t handle;
  NAPI_CALL(env, napi_get_value_bigint_uint64(env, args[0], &handle, &lossless));

  size_t path_len;
  char path_buf[4096];
  NAPI_CALL(env, napi_get_value_string_utf8(env, args[1], path_buf, sizeof(path_buf), &path_len));

  int out_len = 0;
  char *data = skills_fs_read((uintptr_t)handle, path_buf, &out_len);

  napi_value result;
  if (data == NULL) {
    NAPI_CALL(env, napi_get_undefined(env, &result));
    return result;
  }

  NAPI_CALL(env, napi_create_buffer_copy(env, out_len, data, NULL, &result));
  skills_fs_free(data);
  return result;
}

static napi_value Write(napi_env env, napi_callback_info info) {
  size_t argc = 3;
  napi_value args[3];
  NAPI_CALL(env, napi_get_cb_info(env, info, &argc, args, NULL, NULL));

  bool lossless;
  uint64_t handle;
  NAPI_CALL(env, napi_get_value_bigint_uint64(env, args[0], &handle, &lossless));

  size_t path_len;
  char path_buf[4096];
  NAPI_CALL(env, napi_get_value_string_utf8(env, args[1], path_buf, sizeof(path_buf), &path_len));

  void *buf_data;
  size_t buf_len;
  NAPI_CALL(env, napi_get_buffer_info(env, args[2], &buf_data, &buf_len));

  int rc = skills_fs_write((uintptr_t)handle, path_buf, buf_data, (int)buf_len);

  napi_value result;
  NAPI_CALL(env, napi_create_int32(env, rc, &result));
  return result;
}

static napi_value Init(napi_env env, napi_value exports) {
  napi_property_descriptor descs[] = {
      {"createFS", NULL, CreateFS, NULL, NULL, NULL, napi_default, NULL},
      {"shutdown", NULL, Shutdown, NULL, NULL, NULL, napi_default, NULL},
      {"mountBlob", NULL, MountBlob, NULL, NULL, NULL, napi_default, NULL},
      {"read", NULL, Read, NULL, NULL, NULL, napi_default, NULL},
      {"write", NULL, Write, NULL, NULL, NULL, napi_default, NULL},
  };

  NAPI_CALL(env, napi_define_properties(env, exports, sizeof(descs) / sizeof(descs[0]), descs));
  return exports;
}

NAPI_MODULE(NODE_GYP_MODULE_NAME, Init)
