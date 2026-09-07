# Bounded Linux statusline trace

This evidence records one authorized two-case trace against the immutable statusline script from source `6f23da0ad4fa704da8c7bac53e1065030c89a4f1`. The source script SHA-256 check passed. A temporary copy replaced only the global stderr suppression with `set -x`; source files were not edited.

One canned stub render ran with the safe reconstructed exports from the failed full-check controller, and one ran under `env -i` while preserving the same real `HOME` value. Both rendered the same `100 saved` badge, exited zero, created their private cache, and traversed the same commands apart from their expected cache paths and process IDs. The reconstructed case therefore did not reproduce the earlier empty render.

The reconstruction includes the controller's explicit Go, Git, mise, PATH, HOME, and resource-control exports. It did not invoke the test suite's `clean_env` / `run_json` wrapper, does not establish every variable in the original task subprocess, and used a temporary script with tracing in place of stderr suppression. It also cannot recreate the original run's process or filesystem state. The retained failed run suppressed child stderr, so the cause remains unreproduced and no source change is justified by this trace. A future diagnostic must exercise the actual test wrapper around a single canned render.

This used a canned stub only. It did not invoke the product binary, corpus, statusline suite, benchmark, or retrieval study. Controller cleanup and a fresh query found all three validation VMs deallocated.
