M12 deterministic evidence verification
======================================
This package is NOT an exploitation or autonomous penetration-testing engine.
Its only method is deterministic-evidence, consuming the explicitly synthetic
aspm.synthetic-fixture/v1 document from an existing evidence reader.

The caller provides a trusted approval lookup. Approval must match workspace,
finding, method, scope revision, environment and evidence digest, and must be
current. Denied/cancelled/over-budget work does not open evidence. Successful
reads require byte length/hash integrity plus EOF/Close success.

"reproduced" means only that the approved SYNTHETIC fixture's condition is true;
"not-reproduced" means that fixture condition is false. Neither result claims
that a real vulnerability was reproduced, changes false-positive disposition,
or grants permission to close a finding. UI must retain this method distinction.
The result always leaves CloseFinding and FalsePositive false.

There is no target URL, shell, browser, scanner, tool-call executor, subprocess
or arbitrary script interface. Do not wire model output directly into approval.
Tests use a controlled evidence-reader boundary; real S3 integrity/reopen
semantics are separately exercised by the M02 integration suite.
