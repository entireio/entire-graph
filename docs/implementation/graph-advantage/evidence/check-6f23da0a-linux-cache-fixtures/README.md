# Linux cache-fixture diagnosis attempt 1

This retained attempt targeted source commit `6f23da0ad4fa704da8c7bac53e1065030c89a4f1` and the existing pinned source root. It did not reach toolchain inspection or tests and produced no result archive.

The Azure transport operation returned success with empty remote stdout and stderr, while the result blob remained absent before and after. Local inspection found that the controller's global substitution replaced the placeholder inside both the assignment and its literal guard. The resulting bound guard compared the encoded URL with itself. Exit 73 is therefore a source-level inference from the retained template, not an observed remote exit code; Azure transport recorded only its own zero exit.

Attempt counts: one transport attempt, zero observed test invocations, zero product invocations, zero corpus invocations. The validation VM and both worker VMs were deallocated afterward. The original templates and transport evidence are preserved unchanged.
