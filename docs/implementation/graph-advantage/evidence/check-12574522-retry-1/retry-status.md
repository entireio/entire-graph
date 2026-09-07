# Resource-bounded retry status

The one authorized retry used `MISE_JOBS=1`, `GOFLAGS=-p=1`, and
`GOMAXPROCS=2` from the clean pinned source. All `mise run check` stages and
existing race/30-minute package timeout settings were retained. It terminated
before producing a terminal result after logging `fmt` and `vet` startup. The
cause is unknown and is not classified as a source failure or resource failure.
