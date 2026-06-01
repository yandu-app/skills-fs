// callback.c — C helper to invoke provider callbacks from Go.
// CGo cannot call C function pointers directly; this file bridges the gap.

#include <stdint.h>
#include <stdlib.h>

typedef int (*skills_fs_provider_cb)(
    uintptr_t handle,
    const char* action,
    const char* params_json,
    const char* payload,
    int payload_len,
    char** out_data,
    int* out_len
);

int skills_fs_provider_cb_call(
    skills_fs_provider_cb cb,
    uintptr_t handle,
    const char* action,
    const char* params_json,
    const char* payload,
    int payload_len,
    char** out_data,
    int* out_len
) {
    return cb(handle, action, params_json, payload, payload_len, out_data, out_len);
}
