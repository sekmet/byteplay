# M0 scoped leaf

Baseline `bb366d5102085477b7270d7982e60712cc23fb41`. Scope: Baseline characterization: go build, go vet, go test, go test -race, npm ci/build and legacy endpoints.

- [ ] Reconfirm branch drift and the specific L3 acceptance requirements
- [ ] Write failing regression/fixture tests before behavior changes
- [ ] Implement minimal scope; preserve /api/v1 contracts
- [ ] Run gofmt, focused/full tests, race detector and media/UI checks where relevant
- [ ] Record exact evidence in docs/evidence/M0.md and independent review

STOP on unavailable required tool, unverifiable policy, code drift or failing mandatory oracle.
