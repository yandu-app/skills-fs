{
  "targets": [
    {
      "target_name": "skills_fs",
      "sources": ["binding.c"],
      "include_dirs": [
        "<(module_root_dir)/lib"
      ],
      "libraries": [
        "-L<(module_root_dir)/lib",
        "-lgobridge",
        "-Wl,-rpath,\\$$ORIGIN/../../lib"
      ],
      "cflags": ["-Wall", "-Wextra", "-Wno-unused-parameter"]
    }
  ]
}
